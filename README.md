Tool for converting.

```go
if err := testutil.WaitForResult(func() (bool, error) {
	if foo != bar {
		return false, fmt.Errorf("boom")
	}
}); err != nil {
	t.Fatal(err)
}
```

to

```go
for r := retry.OneSec(); r.Next(t.FailNow); {
	if foo != bar {
		t.Log("boom")
		continue
	}
	break
}
```

### Usage

```
convert [-w] file.go ...
```

Uses `apply` package from https://gist.github.com/josharian/78760cea426d7f104c7c55f0b3c037d1

See https://github.com/golang/go/issues/17108 for details.
