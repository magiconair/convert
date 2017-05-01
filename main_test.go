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
			for r := retry.OneSec(); r.NextOr(t.FailNow); {
				break
			}
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
			for r := retry.OneSec(); r.NextOr(t.FailNow); {
				if foo == bar {
					t.Fatal(err)
				}
				break
			}
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
			for r := retry.OneSec(); r.NextOr(t.FailNow); {
				if x > 0 {
					break
				}
				t.Log("foo")
			}
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
			for r := retry.OneSec(); r.NextOr(t.FailNow); {
				if err := g(); err != nil {
					t.Log(err)
					continue
				}
				break
			}
			`,
		},
	}

	clean := func(s string) string {
		s = strings.Trim(s, " \n")
		s = strings.Replace(s, "\t", "", -1)     // drop all tabs
		s = strings.Replace(s, "\n\n", "\n", -1) // replace newlines with ;
		s = strings.Replace(s, "\n", ";", -1)    // replace newlines with ;
		s = strings.Replace(s, "{;", "{ ", -1)
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
