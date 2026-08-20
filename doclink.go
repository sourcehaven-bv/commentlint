package main

import (
	"fmt"
	"go/ast"
	"go/doc/comment"
	"go/token"
	"regexp"
	"sort"
	"strings"
)

// doclink reports doc comments whose [Bracketed.Reference] does not resolve to
// a real symbol.
//
// Go degrades an unresolvable doc link SILENTLY: `go/doc/comment` keeps it as
// plain text, so pkg.go.dev renders the literal characters "[Set.Enforce]",
// brackets and all. Nothing in the toolchain objects — verified against go vet,
// staticcheck and golangci-lint's godoclint, all of which report zero on a
// deliberately broken link. The failure is invisible exactly because rendering
// treats it as fine.
//
// The check inverts the stdlib's own resolver rather than reimplementing symbol
// lookup. `comment.Parser` only emits a [comment.DocLink] when LookupSym
// accepts the reference; anything it rejects survives as [comment.Plain]. So:
// parse once WITH a resolver built from the package's declarations, then look
// for bracketed text that came back as plain. Scoping, method receivers and
// package qualification are the stdlib's problem, not ours.

// bracketRe finds candidate references in the plain text the parser rejected.
// Only the shapes Go itself treats as a doc link are considered: [Name],
// [Recv.Name], [pkg.Name], [pkg.Recv.Name].
var bracketRe = regexp.MustCompile(`\[([A-Za-z_][\w]*(?:\.[A-Za-z_][\w]*){0,2})\](['’]?s\b)?`)

// pluralSuffix marks a reference that failed only because a plural or
// possessive "s" follows the closing bracket.
const pluralSuffix = "\x00plural"

// DocLinkFinding is one bracketed reference that does not resolve.
type DocLinkFinding struct {
	File string
	Line int
	Ref  string
	Doc  string
	Sym  string // the enclosing declaration, for the message
	// Hint is the qualified form when the reference names a method that
	// exists but was written without its receiver -- by far the most common
	// way to write a link Go silently declines to render.
	Hint string
}

// SymbolTable is the set of symbols a package declares, in the shape
// [comment.Parser.LookupSym] expects.
type SymbolTable struct {
	top    map[string]bool            // const, func, type, var
	method map[string]map[string]bool // recv -> method/field
	pkgs   map[string]string          // package name -> import path
	// corpus resolves symbols in OTHER packages of the same repo.
	corpus Corpus
}

// NewSymbolTable collects every symbol declared by the files of one package,
// plus the packages those files import.
//
// Fields and methods both land in the method map: [T.F] is a valid doc link to
// a struct field as well as to a method, and the parser cannot tell them apart.
func NewSymbolTable(files []*ast.File) *SymbolTable {
	st := &SymbolTable{
		top:    map[string]bool{},
		method: map[string]map[string]bool{},
		pkgs:   map[string]string{},
	}
	addMethod := func(recv, name string) {
		if st.method[recv] == nil {
			st.method[recv] = map[string]bool{}
		}
		st.method[recv][name] = true
	}
	for _, f := range files {
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			name := path
			if i := strings.LastIndex(path, "/"); i >= 0 {
				name = path[i+1:]
			}
			if imp.Name != nil {
				name = imp.Name.Name
			}
			st.pkgs[name] = path
		}
		for _, d := range f.Decls {
			switch v := d.(type) {
			case *ast.FuncDecl:
				if v.Recv == nil || len(v.Recv.List) == 0 {
					st.top[v.Name.Name] = true
					continue
				}
				addMethod(recvTypeName(v.Recv.List[0].Type), v.Name.Name)
			case *ast.GenDecl:
				for _, spec := range v.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						st.top[s.Name.Name] = true
						collectFields(s, addMethod)
					case *ast.ValueSpec:
						for _, n := range s.Names {
							st.top[n.Name] = true
						}
					}
				}
			}
		}
	}
	return st
}

// collectFields records struct fields and interface methods as [T.X] targets.
func collectFields(s *ast.TypeSpec, add func(recv, name string)) {
	var list *ast.FieldList
	switch t := s.Type.(type) {
	case *ast.StructType:
		list = t.Fields
	case *ast.InterfaceType:
		list = t.Methods
	default:
		return
	}
	if list == nil {
		return
	}
	for _, f := range list.List {
		for _, n := range f.Names {
			add(s.Name.Name, n.Name)
		}
	}
}

func recvTypeName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.StarExpr:
		return recvTypeName(v.X)
	case *ast.IndexExpr: // generic receiver: T[P]
		return recvTypeName(v.X)
	case *ast.IndexListExpr:
		return recvTypeName(v.X)
	}
	return ""
}

// qualify returns the [Recv.Name] form when a bare reference names a method
// this package declares. Go requires the receiver, so the author almost
// certainly meant the qualified link.
func (st *SymbolTable) qualify(ref string) string {
	if strings.Contains(ref, ".") {
		return ""
	}
	var recvs []string
	for recv, ms := range st.method {
		if ms[ref] && recv != "" {
			recvs = append(recvs, recv+"."+ref)
		}
	}
	if len(recvs) != 1 {
		return "" // ambiguous or absent: no safe suggestion
	}
	return recvs[0]
}

// corpusHas reports whether another package in the corpus declares name.
// An unknown package resolves to TRUE: the reference may be to a dependency
// outside this repo, and reporting those would be guessing.
func (st *SymbolTable) corpusHas(pkg, name string) bool {
	if st.corpus == nil {
		return true
	}
	other, ok := st.corpus[pkg]
	if !ok {
		return true
	}
	return other.top[name]
}

// Parser returns a [comment.Parser] wired to this package's symbols.
func (st *SymbolTable) Parser() *comment.Parser {
	return &comment.Parser{
		LookupSym: func(recv, name string) bool {
			if recv == "" {
				return st.top[name]
			}
			// A dotted reference reaches here two ways: [Type.Method] in
			// this package, or [pkg.Symbol] where pkg is imported. Accept
			// either, and defer to the corpus for the cross-package case.
			if st.method[recv][name] {
				return true
			}
			if _, imported := st.pkgs[recv]; imported {
				return st.corpusHas(recv, name)
			}
			return false
		},
		LookupPackage: func(name string) (string, bool) {
			p, ok := st.pkgs[name]
			return p, ok
		},
	}
}

// Corpus maps a package's import-path suffix to the symbols it declares, so a
// cross-package reference like [entitymanager.Manager] can be resolved. Without
// it every such link is a false positive: LookupSym is package-scoped by
// design, and it is the single largest error class on a real repo (42 of 98).
type Corpus map[string]*SymbolTable

// FindDocLinks reports unresolvable doc links in one package's files.
//
// corpus may be nil, in which case cross-package references are ACCEPTED
// unchecked rather than reported — a reference this tool cannot resolve is not
// evidence the reference is wrong.
func FindDocLinks(fset *token.FileSet, files []*ast.File, names []string, corpus Corpus) []DocLinkFinding {
	st := NewSymbolTable(files)
	st.corpus = corpus
	p := st.Parser()

	var out []DocLinkFinding
	for i, f := range files {
		file := ""
		if i < len(names) {
			file = names[i]
		}
		for _, d := range f.Decls {
			doc, sym := docOf(d)
			if doc == nil {
				continue
			}
			for _, ref := range unresolved(p, doc.Text()) {
				if st.unimportedPackageRef(ref) {
					continue
				}
				out = append(out, DocLinkFinding{
					File: file,
					Line: fset.Position(doc.Pos()).Line,
					Ref:  ref,
					Doc:  doc.Text(),
					Sym:  sym,
					Hint: st.qualify(ref),
				})
			}
		}
		if f.Doc != nil {
			for _, ref := range unresolved(p, f.Doc.Text()) {
				if st.unimportedPackageRef(ref) {
					continue
				}
				out = append(out, DocLinkFinding{
					File: file,
					Line: fset.Position(f.Doc.Pos()).Line,
					Ref:  ref,
					Doc:  f.Doc.Text(),
					Sym:  f.Name.Name,
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

func docOf(d ast.Decl) (*ast.CommentGroup, string) {
	switch v := d.(type) {
	case *ast.FuncDecl:
		return v.Doc, v.Name.Name
	case *ast.GenDecl:
		name := ""
		if len(v.Specs) > 0 {
			name, _ = specName(v.Specs[0])
		}
		return v.Doc, name
	}
	return nil, ""
}

// unresolved returns the bracketed references the parser left as plain text.
//
// Walking the parsed document rather than the raw string is what makes this
// precise: text inside a code block or an indented pre block is never a link,
// and the parser has already excluded it.
func unresolved(p *comment.Parser, text string) []string {
	doc := p.Parse(text)
	var refs []string
	seen := map[string]bool{}
	var walk func([]comment.Text)
	walk = func(ts []comment.Text) {
		for _, t := range ts {
			switch v := t.(type) {
			case comment.Plain:
				for _, m := range bracketRe.FindAllStringSubmatch(string(v), -1) {
					ref := m[1]
					if m[2] != "" {
						// [X]s: Go requires the bracket to end the token,
						// so a pluralized link never renders. Distinct
						// from a missing symbol, and reported as such.
						ref += pluralSuffix
					}
					if isNotASymbol(m[1]) || seen[ref] {
						continue
					}
					seen[ref] = true
					refs = append(refs, ref)
				}
			case comment.Italic:
			case *comment.Link:
				walk(v.Text)
			case *comment.DocLink:
				walk(v.Text)
			}
		}
	}
	for _, blk := range doc.Content {
		switch v := blk.(type) {
		case *comment.Paragraph:
			walk(v.Text)
		case *comment.Heading:
			walk(v.Text)
		case *comment.List:
			for _, it := range v.Items {
				for _, c := range it.Content {
					if pp, ok := c.(*comment.Paragraph); ok {
						walk(pp.Text)
					}
				}
			}
		}
	}
	sort.Strings(refs)
	return refs
}

// isNotASymbol filters bracketed text that was never meant as a doc link:
// predeclared identifiers, and lowercase single words, which are ordinary
// prose or markdown-style placeholders far more often than package names.
func isNotASymbol(ref string) bool {
	if predeclared[ref] {
		return true
	}
	if !strings.Contains(ref, ".") {
		r := ref[0]
		return r >= 'a' && r <= 'z'
	}
	return false
}

// unimportedPackageRef reports whether ref names a package this file does not
// import. Go does not treat those as doc links at all (LookupPackage returns
// false, so LookupSym is never consulted), which means they render as literal
// brackets — but reporting them is wrong for a different reason: they are
// overwhelmingly deliberate cross-references to a package that CANNOT be
// imported without a cycle, e.g. [entitymanager.Manager] named from the acl
// package it calls into. Naming the collaborator is the point; the author is
// not claiming the link renders.
func (st *SymbolTable) unimportedPackageRef(ref string) bool {
	ref = strings.TrimSuffix(ref, pluralSuffix)
	head, rest, ok := strings.Cut(ref, ".")
	if !ok || rest == "" {
		return false
	}
	if r := head[0]; r < 'a' || r > 'z' {
		return false // [Type.Method], not [pkg.Sym]
	}
	_, imported := st.pkgs[head]
	return !imported
}

// predeclared are Go's builtin identifiers. [any] and [string] are valid Go but
// never doc links to anything a package declares.
var predeclared = map[string]bool{
	"any": true, "bool": true, "byte": true, "comparable": true,
	"complex64": true, "complex128": true, "error": true, "float32": true,
	"float64": true, "int": true, "int8": true, "int16": true, "int32": true,
	"int64": true, "rune": true, "string": true, "uint": true, "uint8": true,
	"uint16": true, "uint32": true, "uint64": true, "uintptr": true,
	"true": true, "false": true, "iota": true, "nil": true,
}

func (f DocLinkFinding) String(rel func(string) string) string {
	var sb strings.Builder
	if ref, ok := strings.CutSuffix(f.Ref, pluralSuffix); ok {
		fmt.Fprintf(&sb, "%s:%d: [doclink] %s writes [%s]s — a doc link must END at the bracket, so the trailing \"s\" stops it rendering. Use [%s] values, or rephrase.\n",
			rel(f.File), f.Line, f.Sym, ref, ref)
	} else if f.Hint != "" {
		fmt.Fprintf(&sb, "%s:%d: [doclink] %s writes [%s], but that is a METHOD — a doc link needs its receiver. Use [%s].\n",
			rel(f.File), f.Line, f.Sym, f.Ref, f.Hint)
	} else {
		fmt.Fprintf(&sb, "%s:%d: [doclink] %s documents [%s], which resolves to nothing — godoc renders the brackets literally.\n",
			rel(f.File), f.Line, f.Sym, f.Ref)
	}
	fmt.Fprintf(&sb, "    │ %s\n", wrapExcerpt(f.Doc))
	return sb.String()
}
