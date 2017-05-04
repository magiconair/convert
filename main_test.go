package main

import (
	"strings"
	"testing"
)

func TestRewriteBody(t *testing.T) {

	tests := []struct {
		desc, in, out string
	}{
		{
			"empty body",
			`
			if err := testutil.WaitForResult(func() (bool, error) {
				return true, nil
			}); err != nil {
				t.Fatal(err)
			}
			`,
			`
			retry.Run("", t, func(r *retry.R) { })
			`,
		},
		{
			"if with t.Fatal",
			`
			if err := testutil.WaitForResult(func() (bool, error) {
				if foo == bar {
					t.Fatal(err)
				}
				return true, nil
			}); err != nil {
				t.Fatal(err)
			}
			`,
			`
			retry.Run("", t, func(r *retry.R) {
				if foo == bar {
					r.Fatal(err)
				}
			})
			`,
		},
		{
			"return with binary expr",
			`
			if err := testutil.WaitForResult(func() (bool, error) {
				return x > 0, "foo"
			}); err != nil {
				t.Fatal(err)
			}
			`,
			`
			retry.Run("", t, func(r *retry.R) {
				if x <= 0 {
					r.Fatal("foo")
				}
			})
			`,
		},
		{
			"return with binary expr and func",
			`
			if err := testutil.WaitForResult(func() (bool, error) {
				return len(s1.WANMembers()) > 1, nil
			}); err != nil {
				t.Fatal(err)
			}
			`,
			`
			retry.Run("", t, func(r *retry.R) {
				if len(s1.WANMembers()) <= 1 {
					r.Fatal(nil)
				}
			})
			`,
		},
		{
			"wfr with local fn",
			`
			g := func() (bool, error) { return true, nil }
			if err := testutil.WaitForResult(g); err != nil {
				t.Fatal(err)
			}
			`,
			`
			g := func() (bool, error) { return true, nil }
			retry.Run("", t, func(r *retry.R) {
				if err := g(); err != nil {
					r.Fatal(err)
				}
			})
			`,
		},
	}

	clean := func(s string) string {
		s = strings.Trim(s, " \n")
		s = strings.Replace(s, "\t", "", -1)     // drop all tabs
		s = strings.Replace(s, "\n\n", "\n", -1) // replace newlines with ;
		s = strings.Replace(s, "\n", ";", -1)    // replace newlines with ;
		s = strings.Replace(s, "{;", "{ ", -1)
		s = strings.Replace(s, ";}", " }", -1)
		s = strings.Replace(s, "};", "} ", -1)
		s = strings.Replace(s, ";;", ";", -1)
		return s
	}

	wrap := func(s string) string {
		return "package foo\nfunc f() {\n" + s + "}"
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			data, err := transformFile("src.go", wrap(tt.in))
			if err != nil {
				t.Fatal(err)
			}
			if got, want := clean(string(data)), clean(wrap(tt.out)); got != want {
				t.Fatalf("got \n%q\nwant\n%q\n", got, want)
			}
		})
	}
}
