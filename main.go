// wfr2retry rewrites calls from WaitForResult to use the retry package.
//
// It transforms from
//
//   if err := testutil.WaitForResult(func() (bool, error) {
//       if err := foo(); err != nil {
//           return false, fmt.Errorf("foo: %s", err)
//       }
//       return true, nil
//   }); err != nil {
//       t.Fatal(err)
//   }
//
// to
//
//   retry.Run("", t, func(r *retry.R) {
//       if err := foo(); err != nil {
//           t.Fatalf("foo: %s", err)
//       }
//   })
//
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

	"github.com/magiconair/wfr2retry/apply"
)

var write, printAST bool

func main() {
	flag.BoolVar(&write, "w", false, "write changes to file")
	flag.BoolVar(&printAST, "ast", false, "print ast and exit")
	flag.Parse()

	log.SetFlags(0)
	log.SetPrefix("***** ")

	for _, fname := range flag.Args() {
		data, err := transformFile(fname, nil)
		if err != nil {
			log.Fatal(err)
		}
		if write {
			if err := ioutil.WriteFile(fname, data, 0644); err != nil {
				log.Fatal(err)
			}
		} else {
			os.Stdout.Write(data)
		}
	}
}

func transformFile(fname string, src interface{}) ([]byte, error) {
	// parse input
	fset := token.NewFileSet()
	root, err := parser.ParseFile(fset, fname, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	// not pretty ... :(
	if printAST {
		ast.Print(fset, root)
		os.Exit(0)
	}

	// apply transformation
	// todo(fs): we probably need to fix the imports or run goimports afterwards
	apply.Apply(root, rewrite, nil)

	// format transformed code
	var b bytes.Buffer
	if err := format.Node(&b, fset, root); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// rewrite recursively rewrites the if statements
// which use the testutil.WaitForResult construct
// and replaces them with a for loop which uses
// the retry package.
func rewrite(c apply.ApplyCursor) bool {
	switch c.Node().(type) {
	case *ast.IfStmt:
		var body *ast.BlockStmt
		arg := wfrBody(c.Node())
		switch x := arg.(type) {
		case *ast.Ident:
			body = makeSimpleBody(x)
		case *ast.BlockStmt:
			body = rewriteBody(x)
		default:
			return true
		}
		c.Replace(makeRetryRun(body))
	}
	return true
}

func makeSimpleBody(s *ast.Ident) *ast.BlockStmt {
	return &ast.BlockStmt{
		List: []ast.Stmt{
			&ast.IfStmt{
				Init: &ast.AssignStmt{
					Lhs: []ast.Expr{
						&ast.Ident{Name: "err"},
					},
					Tok: token.DEFINE,
					Rhs: []ast.Expr{
						&ast.CallExpr{Fun: s},
					},
				},
				Cond: &ast.BinaryExpr{
					X:  &ast.Ident{Name: "err"},
					Op: token.NEQ,
					Y:  &ast.Ident{Name: "nil"},
				},
				Body: &ast.BlockStmt{
					List: []ast.Stmt{
						&ast.ExprStmt{
							&ast.CallExpr{
								Fun: &ast.SelectorExpr{
									X:   &ast.Ident{Name: "r"},
									Sel: &ast.Ident{Name: "Fatal"},
								},
								Args: []ast.Expr{
									&ast.Ident{Name: "err"},
								},
							},
						},
					},
				},
			},
		},
	}
}

// wfrBody checks if the node is an if statement
// of the form and returns the body of the callback function.
// or the name of the test function.
func wfrBody(n ast.Node) ast.Node {
	// if init; cond { body } ?
	if ifn, ok := n.(*ast.IfStmt); ok && ifn.Init != nil && ifn.Body != nil {

		// if a := b ; ... ?
		if a, ok := ifn.Init.(*ast.AssignStmt); ok && len(a.Lhs) == 1 && len(a.Rhs) == 1 {

			// if err := ?
			if a.Lhs[0].(*ast.Ident).Name == "err" {

				// if err := f(a);
				if c, ok := a.Rhs[0].(*ast.CallExpr); ok && len(c.Args) == 1 {

					// if err := (test*).WaitForResult(...) ?
					if f, ok := c.Fun.(*ast.SelectorExpr); ok && f.Sel.Name == "WaitForResult" {

						switch arg0 := c.Args[0].(type) {
						// if err := (test*).WaitForResult(someFunc); ...
						case *ast.Ident:
							return arg0

							// if err := (test*).WaitForResult(func() (bool, error) {...}); ...
						case *ast.FuncLit:
							return arg0.Body

						default:
							log.Fatal("invalid WaitForResult arg type: %T", arg0)
						}
					}
				}
			}
		}
	}
	return n
}

func makeRetryRun(body *ast.BlockStmt) ast.Node {
	return &ast.ExprStmt{
		X: &ast.CallExpr{
			Fun: &ast.SelectorExpr{
				X:   &ast.Ident{Name: "retry"},
				Sel: &ast.Ident{Name: "Run"},
			},
			Args: []ast.Expr{
				&ast.BasicLit{Kind: token.STRING, Value: `""`},
				&ast.Ident{Name: "t"},
				&ast.FuncLit{
					Type: &ast.FuncType{
						Params: &ast.FieldList{
							List: []*ast.Field{
								&ast.Field{
									Names: []*ast.Ident{
										&ast.Ident{Name: "r"},
									},
									Type: &ast.SelectorExpr{
										X:   &ast.Ident{Name: "*retry"},
										Sel: &ast.Ident{Name: "R"},
									},
								},
							},
						},
					},
					Body: body,
				},
			},
		},
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
// return true, val -> drop
// return false, val -> continue // do we have this?
// return expr, val -> if !expr { r.Fatal(val) }
func rewriteReturn(s *ast.ReturnStmt) (stmts []ast.Stmt) {
	// define negations of operations
	notOp := map[token.Token]token.Token{
		token.EQL: token.NEQ, // ! == => !=
		token.GTR: token.LEQ, // ! > => <=
		token.GEQ: token.LSS, // ! >= => <
	}
	// auto-generate the inverse operations
	for k, v := range notOp {
		notOp[v] = k
	}

	// ast.Print(token.NewFileSet(), s.Results)
	switch x := s.Results[0].(type) {
	case *ast.Ident:
		if x.Name == "true" {
			return []ast.Stmt{}
		}

	case *ast.BinaryExpr, *ast.CallExpr:
		var args []ast.Expr
		switch x := s.Results[1].(type) {
		case *ast.Ident:
			args = []ast.Expr{x}

		case *ast.BasicLit:
			args = []ast.Expr{x}

		case *ast.CallExpr:
			fn := x.Fun.(*ast.SelectorExpr)
			fname := fn.X.(*ast.Ident).Name + "." + fn.Sel.Name
			if fname == "t.Fatalf" || fname == "fmt.Errorf" {
				args = x.Args
			} else {
				args = []ast.Expr{x}
			}

		default:
			log.Fatalf("unsupported result type %T", s.Results[1])
		}

		var cond ast.Expr
		if be, ok := x.(*ast.BinaryExpr); ok {
			invop, ok2 := notOp[be.Op]
			if !ok2 {
				log.Fatal("no negation for token ", be.Op)
			}
			be.Op = invop
			cond = be
		} else {
			cond = &ast.UnaryExpr{Op: token.NOT, X: x}
		}

		logf := "Fatalf"
		if len(args) == 1 {
			logf = "Fatal"
		}

		return []ast.Stmt{
			&ast.IfStmt{
				Cond: cond,
				Body: &ast.BlockStmt{
					List: []ast.Stmt{
						&ast.ExprStmt{
							X: &ast.CallExpr{
								Fun: &ast.SelectorExpr{
									X:   &ast.Ident{Name: "r"},
									Sel: &ast.Ident{Name: logf},
								},
								Args: args,
							},
						},
					},
				},
			},
		}

	default:
		log.Fatalf("unsupported result type %T", s.Results[0])
	}
	return
}

// rewrite if statements in the callback
//
// if cond { return false, fmt.Errorf(f, a) } -> if cond { retry.Fatalf(f, a) }
// if cond { return false, fmt.Errorf(f) } -> if cond { retry.Fatal(f) }
// if cond { return false, val } -> if cond { retry.Fatal(val) }
// if cond { t.Fatal(err) } -> if cond { r.Fatal(err) }
func rewriteIf(s *ast.IfStmt) {
	// ast.Print(token.NewFileSet(), s)
	n := len(s.Body.List)
	if n == 0 {
		return
	}
	last := s.Body.List[n-1]
	switch x := last.(type) {
	case *ast.ExprStmt:
		c, ok := x.X.(*ast.CallExpr)
		if !ok {
			return
		}
		// hack: swap t.(Fatal|Fatalf) -> r.(Fatal|Fatalf)
		fn := c.Fun.(*ast.SelectorExpr)
		if fn.X.(*ast.Ident).Name == "t" {
			fn.X.(*ast.Ident).Name = "r"
		}
	case *ast.ReturnStmt:
		// return (true|false), x ?
		if len(x.Results) != 2 {
			return
		}

		// fmt.Errorf(format) -> r.Fatal(format)
		// fmt.Errorf(format, args) -> r.Fatalf(format, args)
		logf := "Fatalf"
		verr := x.Results[1]
		args := []ast.Expr{verr}
		if ce, ok := verr.(*ast.CallExpr); ok {
			if f, ok2 := ce.Fun.(*ast.SelectorExpr); ok2 {
				if f.X.(*ast.Ident).Name == "fmt" && f.Sel.Name == "Errorf" {
					args = ce.Args
				}
			}
		}
		if len(args) == 1 {
			logf = "Fatal"
		}

		s.Body.List = []ast.Stmt{
			&ast.ExprStmt{
				X: &ast.CallExpr{
					Fun: &ast.SelectorExpr{
						X:   &ast.Ident{Name: "r"},
						Sel: &ast.Ident{Name: logf},
					},
					Args: args,
				},
			},
		}
	}

	// the error value

}
