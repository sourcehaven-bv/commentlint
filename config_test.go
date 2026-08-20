package main

import "testing"

func TestParseFileConfig(t *testing.T) {
	src := `
# a comment
exclude:
  - "internal/legacy/**"
  - testdata
rules:
  too-long: false
  nil-contract: true
ignore:
  nil-contract:
    - "internal/store/**"
allow-phrases:
  - "non-nil empty slice"
settings:
  max-body-lines: 6
  dup-overlap: 0.35
`
	cfg, err := parseFileConfig(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cfg.Exclude) != 2 {
		t.Fatalf("exclude = %v, want 2", cfg.Exclude)
	}
	if cfg.Rules["too-long"] || !cfg.Rules["nil-contract"] {
		t.Fatalf("rules = %v", cfg.Rules)
	}
	if got := cfg.Ignore["nil-contract"]; len(got) != 1 || got[0] != "internal/store/**" {
		t.Fatalf("ignore = %v", cfg.Ignore)
	}
	if cfg.Ints["max-body-lines"] != 6 {
		t.Fatalf("ints = %v", cfg.Ints)
	}
	if cfg.Floats["dup-overlap"] != 0.35 {
		t.Fatalf("floats = %v", cfg.Floats)
	}
	if !cfg.AllowedPhrase("decodes to a NON-NIL EMPTY SLICE here") {
		t.Fatal("allow-phrase should match case-insensitively")
	}
}

func TestParseFileConfigErrors(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{"bad bool", "rules:\n  too-long: yes-please\n"},
		{"bad number", "settings:\n  max-body-lines: lots\n"},
		{"orphan list", "ignore:\n  - \"x/**\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseFileConfig(tc.src); err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestMatchAnyGlob(t *testing.T) {
	for _, tc := range []struct {
		name  string
		globs []string
		path  string
		want  bool
	}{
		{"doublestar prefix", []string{"internal/legacy/**"}, "internal/legacy/a/b.go", true},
		{"doublestar miss", []string{"internal/legacy/**"}, "internal/live/b.go", false},
		{"bare segment", []string{"testdata"}, "a/testdata/b.go", true},
		{"exact", []string{"cmd/main.go"}, "cmd/main.go", true},
		{"no globs", nil, "a.go", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchAnyGlob(tc.globs, tc.path); got != tc.want {
				t.Fatalf("matchAnyGlob(%v, %q) = %v, want %v", tc.globs, tc.path, got, tc.want)
			}
		})
	}
}

func TestInlineIgnores(t *testing.T) {
	t.Run("bare suppresses all", func(t *testing.T) {
		rules, ok := InlineIgnores("func F() {} //commentlint:ignore")
		if !ok || rules != nil {
			t.Fatalf("rules=%v ok=%v", rules, ok)
		}
	})
	t.Run("named rules", func(t *testing.T) {
		rules, ok := InlineIgnores("func F() {} //commentlint:ignore nil-contract, too-long -- why")
		if !ok || !rules["nil-contract"] || !rules["too-long"] {
			t.Fatalf("rules=%v ok=%v", rules, ok)
		}
	})
	t.Run("absent", func(t *testing.T) {
		if _, ok := InlineIgnores("func F() {}"); ok {
			t.Fatal("want no directive")
		}
	})
}

func TestSuppressed(t *testing.T) {
	fc, err := parseFileConfig("ignore:\n  nil-contract:\n    - \"internal/store/**\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if !suppressed(fc, "nil-contract", "internal/store/pg.go", "doc") {
		t.Fatal("path ignore should suppress")
	}
	if suppressed(fc, "nil-contract", "internal/acl/a.go", "doc") {
		t.Fatal("unrelated path must not be suppressed")
	}
	if !suppressed(fc, "nil-contract", "internal/acl/a.go", "doc\n//commentlint:ignore nil-contract") {
		t.Fatal("inline directive should suppress")
	}
	if suppressed(fc, "too-long", "internal/acl/a.go", "doc\n//commentlint:ignore nil-contract") {
		t.Fatal("named directive must not suppress a different rule")
	}
}
