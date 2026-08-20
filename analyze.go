package main

import (
	"go/ast"
	"go/token"
	"regexp"
	"strings"
)

type Level int

const (
	LevelPackage Level = iota
	LevelExportedDecl
	LevelUnexportedDecl
	LevelField
	LevelBody
)

func (l Level) String() string {
	switch l {
	case LevelPackage:
		return "package"
	case LevelExportedDecl:
		return "exported-decl"
	case LevelUnexportedDecl:
		return "unexported-decl"
	case LevelField:
		return "field"
	default:
		return "body"
	}
}

// Comment is one comment group with the scope it is attached to.
type Comment struct {
	Pos         token.Pos
	End         token.Pos
	Level       Level
	Text        string   // comment text, markers stripped
	Ident       string   // identifier it documents, if any
	Scope       []string // identifier tokens in scope at this position
	Lines       int
	File        string
	LineNo      int
	IsDirective bool
}

type Finding struct {
	File    string
	Line    int
	Level   Level
	Rule    string
	Score   float64
	Message string
	Excerpt string
	Outside []string // high-IDF tokens that name things outside this scope
}

var (
	directiveRe = regexp.MustCompile(`^(go:|nolint|scupper:|lint:|noinspection|\+build|export |cgo)`)
	generatedRe = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)
	typedRe     = regexp.MustCompile(`^(Why|Ref|Note|TODO|FIXME|Deprecated)\b`)
	whyKindRe   = regexp.MustCompile(`^Why\(([a-z]+)\):`)
	removalRe   = regexp.MustCompile(`(?i)(remove|drop|delete|revisit|until|once|when)\b|#\d+|go1\.\d+|https?://`)
	codeFragRe  = regexp.MustCompile(`^[\s}{)(]*$|^\w+\s*[:=]|^(if|for|switch|return|func|var|const|go|defer)\b`)
)

// collectComments walks a file and attaches every comment group to the
// narrowest scope containing it.
func collectComments(fset *token.FileSet, f *ast.File, filename string) []Comment {
	if len(f.Comments) > 0 && generatedRe.MatchString(strings.Split(commentRaw(f.Comments[0]), "\n")[0]) {
		return nil
	}

	var out []Comment
	cmap := ast.NewCommentMap(fset, f, f.Comments)

	// Package doc.
	if f.Doc != nil {
		out = append(out, mkComment(fset, f.Doc, LevelPackage, f.Name.Name, nil, filename))
	}

	for node, groups := range cmap {
		for _, g := range groups {
			if f.Doc != nil && g == f.Doc {
				continue
			}
			lvl, ident, scope := classify(node, g, f)
			out = append(out, mkComment(fset, g, lvl, ident, scope, filename))
		}
	}
	return out
}

func classify(node ast.Node, g *ast.CommentGroup, f *ast.File) (Level, string, []string) {
	switch n := node.(type) {
	case *ast.FuncDecl:
		if n.Doc == g {
			lvl := LevelUnexportedDecl
			if n.Name.IsExported() {
				lvl = LevelExportedDecl
			}
			return lvl, n.Name.Name, funcScope(n)
		}
		return LevelBody, "", funcScope(n)
	case *ast.GenDecl:
		if n.Doc == g && len(n.Specs) > 0 {
			name, exported := specName(n.Specs[0])
			if exported {
				return LevelExportedDecl, name, nil
			}
			return LevelUnexportedDecl, name, nil
		}
	case *ast.Field:
		name := ""
		exported := false
		if len(n.Names) > 0 {
			name = n.Names[0].Name
			exported = n.Names[0].IsExported()
		}
		if exported {
			return LevelField, name, nil
		}
		return LevelField, name, nil
	case *ast.TypeSpec:
		if n.Name.IsExported() {
			return LevelExportedDecl, n.Name.Name, nil
		}
		return LevelUnexportedDecl, n.Name.Name, nil
	case *ast.ValueSpec:
		if len(n.Names) > 0 {
			if n.Names[0].IsExported() {
				return LevelExportedDecl, n.Names[0].Name, nil
			}
			return LevelUnexportedDecl, n.Names[0].Name, nil
		}
	}
	// Anything inside a body, or unattached: treat as statement level and
	// find the enclosing function for scope.
	return LevelBody, "", enclosingScope(f, g.Pos())
}

func specName(s ast.Spec) (string, bool) {
	switch v := s.(type) {
	case *ast.TypeSpec:
		return v.Name.Name, v.Name.IsExported()
	case *ast.ValueSpec:
		if len(v.Names) > 0 {
			return v.Names[0].Name, v.Names[0].IsExported()
		}
	}
	return "", false
}

// funcScope returns every identifier visible from inside a function: its name,
// receiver, params, results, and all identifiers used in its body.
func funcScope(fn *ast.FuncDecl) []string {
	var ids []string
	ids = append(ids, fn.Name.Name)
	ast.Inspect(fn, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.Ident:
			ids = append(ids, v.Name)
		case *ast.SelectorExpr:
			ids = append(ids, v.Sel.Name)
		}
		return true
	})
	return ids
}

func enclosingScope(f *ast.File, pos token.Pos) []string {
	var best *ast.FuncDecl
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok {
			if fn.Pos() <= pos && pos <= fn.End() {
				best = fn
			}
		}
	}
	if best == nil {
		return nil
	}
	return funcScope(best)
}

func mkComment(fset *token.FileSet, g *ast.CommentGroup, lvl Level, ident string, scope []string, filename string) Comment {
	raw := commentRaw(g)
	p := fset.Position(g.Pos())
	return Comment{
		Pos:         g.Pos(),
		End:         g.End(),
		Level:       lvl,
		Text:        raw,
		Ident:       ident,
		Scope:       scope,
		Lines:       strings.Count(raw, "\n") + 1,
		File:        filename,
		LineNo:      p.Line,
		IsDirective: isDirective(g),
	}
}

func commentRaw(g *ast.CommentGroup) string {
	var b strings.Builder
	for i, c := range g.List {
		t := c.Text
		switch {
		case strings.HasPrefix(t, "//"):
			t = strings.TrimPrefix(t, "//")
		case strings.HasPrefix(t, "/*"):
			t = strings.TrimSuffix(strings.TrimPrefix(t, "/*"), "*/")
		}
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(strings.TrimSpace(t))
	}
	return b.String()
}

func isDirective(g *ast.CommentGroup) bool {
	for _, c := range g.List {
		body := strings.TrimPrefix(c.Text, "//")
		if directiveRe.MatchString(strings.TrimSpace(body)) || directiveRe.MatchString(body) {
			return true
		}
	}
	return false
}
