package main

import (
	"bytes"
	"flag"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/ioutil"
	"log"
	"os"
	"reflect"

	"github.com/magiconair/convert/apply"
)

func main() {
	write := flag.Bool("w", false, "write changes to file")
	flag.Parse()

	log.SetFlags(0)
	log.SetPrefix("***** ")

	for _, fname := range flag.Args() {
		data, err := transformFile(fname, nil)
		if err != nil {
			log.Fatal(err)
		}
		if *write {
			if err := ioutil.WriteFile(fname, data, 0644); err != nil {
				log.Fatal(err)
			}
		} else {
			os.Stdout.Write(data)
		}
	}
}

func transformFile(fname string, src interface{}) ([]byte, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, fname, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	transform(f)
	var b bytes.Buffer
	if err := format.Node(&b, fset, f); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func transform(root ast.Node) {
	apply.Apply(root, rewrite, nil)
	// we probably need to fix the imports
}

// rewrite recursively rewrites the if statements
// which use the testutil.WaitForResult construct
// and replaces them with a for loop which uses
// the retry package.
func rewrite(c apply.ApplyCursor) bool {
	switch c.Node().(type) {
	case *ast.IfStmt:
		if body := isWaitForResult(c.Node()); body != nil {
			stmt := makeForRetry(rewriteBody(body))
			c.Replace(stmt)
		}
	}
	return true
}

// isWaitForResult checks if the node is an if statement
// of the form and returns the body of the callback function.
//
//   if err := testutil.WaitForResult(func() (bool, error) {
//       // callback body
//   }); err != nil {
//       t.Fatal(err)
//   }

func isWaitForResult(n ast.Node) *ast.BlockStmt {
	// if stmt?
	x, ok := n.(*ast.IfStmt)
	if !ok || x.Init == nil || x.Body == nil {
		// log.Print("not if")
		return nil
	}

	// if x := y; ... ?
	a, ok := x.Init.(*ast.AssignStmt)
	if !ok || len(a.Lhs) != 1 || len(a.Rhs) != 1 {
		// log.Print("wrong args")
		return nil
	}

	// if err := ?
	var err error
	if a.Lhs[0].(*ast.Ident).Obj.Type != reflect.TypeOf(err) {
		// log.Print("no error")
		return nil
	}

	// if err := f(x) ?
	c, ok := a.Rhs[0].(*ast.CallExpr)
	if !ok || len(c.Args) != 1 {
		// log.Print("no call")
		return nil
	}

	// if err := testutil.WaitForResult(...) ?
	f, ok := c.Fun.(*ast.SelectorExpr)
	if !ok || f.X.(*ast.Ident).Name != "testutil" || f.Sel.Name != "WaitForResult" {
		// log.Print("wrong name")
		return nil
	}

	// return function body
	ff, ok := c.Args[0].(*ast.FuncLit)
	if !ok {
		// log.Print("no func lit")
		return nil
	}
	return ff.Body
}

// makeForRetry creates a for loop with a retryer
// which replaces the if stmt with testutil.WaitForResult.
// It expects a body that is rewritten for the for loop.
func makeForRetry(body *ast.BlockStmt) ast.Node {
	return &ast.ForStmt{
		Init: &ast.AssignStmt{
			Lhs: []ast.Expr{
				&ast.Ident{Name: "r"},
			},
			Tok: token.DEFINE,
			Rhs: []ast.Expr{
				&ast.CallExpr{
					Fun: &ast.SelectorExpr{
						X:   &ast.Ident{Name: "retry"},
						Sel: &ast.Ident{Name: "OneSec"},
					},
				},
			},
		},
		Cond: &ast.CallExpr{
			Fun: &ast.SelectorExpr{
				X:   &ast.Ident{Name: "r"},
				Sel: &ast.Ident{Name: "NextOr"},
			},
			Args: []ast.Expr{
				&ast.SelectorExpr{
					X:   &ast.Ident{Name: "t"},
					Sel: &ast.Ident{Name: "FailNow"},
				},
			},
		},
		Body: body,
	}
}

// rewriteBody transforms the body of the
// WaitForResult(func() (bool, error) {...})
// callback.
func rewriteBody(n ast.Node) *ast.BlockStmt {
	body, ok := n.(*ast.BlockStmt)
	if !ok {
		panic("not a block stmt")
	}

	bs := &ast.BlockStmt{}
OUTER:
	for _, x := range body.List {
		switch s := x.(type) {
		case *ast.IfStmt:
			rewriteIf(s)

		case *ast.ReturnStmt:
			bs.List = append(bs.List, rewriteReturn(s)...)
			continue OUTER
		}
		bs.List = append(bs.List, x)
	}
	return bs
}

// rewrite return statements
//
// return true, val -> break
// return false, val -> continue // do we have this?
// return expr, val -> if expr { break } t.Log(val)
func rewriteReturn(s *ast.ReturnStmt) (stmts []ast.Stmt) {
	if vbool, ok := s.Results[0].(*ast.Ident); ok {
		if vbool.Name == "true" {
			return []ast.Stmt{&ast.BranchStmt{Tok: token.BREAK}}
		} else {
			return []ast.Stmt{&ast.BranchStmt{Tok: token.CONTINUE}}
		}
	}

	if expr, ok := s.Results[0].(*ast.BinaryExpr); ok {
		return []ast.Stmt{
			&ast.IfStmt{
				Cond: expr,
				Body: &ast.BlockStmt{
					List: []ast.Stmt{
						&ast.BranchStmt{Tok: token.BREAK},
					},
				},
			},
			&ast.ExprStmt{
				X: &ast.CallExpr{
					Fun: &ast.SelectorExpr{
						X:   &ast.Ident{Name: "t"},
						Sel: &ast.Ident{Name: "Log"},
					},
					Args: []ast.Expr{s.Results[1]},
				},
			},
		}
	}
	panic("unsupported return")
}

// rewrite if statements in the callback
//
// if cond { return false, fmt.Errorf(f, a) } -> if cond { t.Logf(f, a); continue }
// if cond { return false, fmt.Errorf(f) } -> if cond { t.Log(f); continue }
// if cond { return false, val } -> if cond { t.Log(val); continue }
func rewriteIf(s *ast.IfStmt) {
	if len(s.Body.List) != 1 {
		return
	}
	ret, ok := s.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 2 {
		return
	}

	// the error value
	verr := ret.Results[1]

	logf := "Logf"
	args := []ast.Expr{verr}

	// simplify fmt.Errorf(format, args) to format, args
	if ce, ok := verr.(*ast.CallExpr); ok {
		if f, ok2 := ce.Fun.(*ast.SelectorExpr); ok2 {
			if f.X.(*ast.Ident).Name == "fmt" && f.Sel.Name == "Errorf" {
				args = ce.Args
			}
		}
	}

	if len(args) == 1 {
		logf = "Log"
	}

	stmts := []ast.Stmt{
		&ast.ExprStmt{
			X: &ast.CallExpr{
				Fun: &ast.SelectorExpr{
					X:   &ast.Ident{Name: "t"},
					Sel: &ast.Ident{Name: logf},
				},
				Args: args,
			},
		},
	}

	vbool := ret.Results[0].(*ast.Ident).Name
	if vbool == "false" {
		stmts = append(stmts, &ast.BranchStmt{Tok: token.CONTINUE})
	} else {
		stmts = append(stmts, &ast.BranchStmt{Tok: token.BREAK})
	}

	s.Body.List = stmts
}
