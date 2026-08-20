package main

import (
	"go/parser"
	"go/token"
	"testing"
)

// commentsFor parses src and returns its comments, as the analyzer sees them.
func commentsFor(t *testing.T, src string) []Comment {
	t.Helper()
	dir := t.TempDir() + "/a.go"
	if err := writeFile(dir, src); err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, dir, src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	return collectComments(fset, f, dir)
}

func TestFindParamContracts(t *testing.T) {
	t.Run("primitive param with precondition is reported", func(t *testing.T) {
		got := FindParamContracts(commentsFor(t, `package a
// store writes creds. repoPath MUST already have passed containedPath.
func store(repoPath string) {}
`))
		if len(got) != 1 || got[0].C.Ident != "store" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("structured param is not reported", func(t *testing.T) {
		got := FindParamContracts(commentsFor(t, `package a
type Safe struct{}
// store writes creds. p MUST already have passed containedPath.
func store(p Safe) {}
`))
		if len(got) != 0 {
			t.Fatalf("a named type carries the invariant; got %+v", got)
		}
	})
	t.Run("precondition naming no parameter is not reported", func(t *testing.T) {
		got := FindParamContracts(commentsFor(t, `package a
// store writes creds. The caller MUST already have locked the mutex.
func store(repoPath string) {}
`))
		if len(got) != 0 {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestFindNilContracts(t *testing.T) {
	t.Run("prose is reported with a suggestion", func(t *testing.T) {
		got := FindNilContracts(commentsFor(t, `package a
// F does a thing. meta may be nil, disabling validation.
func F(meta *int) *int { return meta }
`))
		if len(got) != 1 || got[0].Kind != "accepted" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("standard form converges", func(t *testing.T) {
		got := FindNilContracts(commentsFor(t, `package a
// F does a thing.
//
// Nil: accepted — a nil meta disables validation.
func F(meta *int) *int { return meta }
`))
		if len(got) != 0 {
			t.Fatalf("tagged comment must not re-fire; got %+v", got)
		}
	})
	t.Run("error-only return is the idiom, not a contract", func(t *testing.T) {
		got := FindNilContracts(commentsFor(t, `package a
// F validates. Returns nil if the config is valid.
func F() error { return nil }
`))
		if len(got) != 0 {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("bare non-nil is not a contract", func(t *testing.T) {
		got := FindNilContracts(commentsFor(t, `package a
// F decodes. An absent key is nil; `+"`x: []`"+` decodes to a non-nil empty slice.
func F() []int { return nil }
`))
		if len(got) != 0 {
			t.Fatalf("descriptive non-nil must not fire; got %+v", got)
		}
	})
}

func TestFindDuplication(t *testing.T) {
	src := `package a
// A explains that the read gate hands back a nop gate under both nop and
// read-only ACLs, so a predicate written against it fails open entirely.
func A() {}

// B explains that the read gate hands back a nop gate under both nop and
// read-only ACLs, so a predicate written against it fails open entirely.
func B() {}
`
	got := FindDuplication(commentsFor(t, src), DefaultDupConfig())
	if len(got) == 0 {
		t.Fatal("identical paragraphs in two comments must be reported")
	}
}
