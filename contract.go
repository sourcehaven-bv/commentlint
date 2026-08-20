package main

import (
	"fmt"
	"go/ast"
	"regexp"
	"sort"
	"strings"
)

// Contract rules cover invariants a comment asserts that the compiler is not
// checking. There are two distinct failures, and they want opposite remedies.
//
// param-contract: the comment states a PRECONDITION about a parameter whose
// type cannot express it ("repoPath MUST already have passed containedPath",
// on a bare string). The remedy is a type. This repo already does it where the
// blast radius is largest — principal.Principal keeps roles/orgID unexported so
// they can only enter through VerifiedFrom — so the rule is pointing at code
// that did not get the treatment its neighbours did, not proposing a new idea.
//
// nil-contract: the comment states a NIL behaviour, which in Go no type can
// express. A wrapper here would be worse than the comment, so the remedy is a
// convention instead: one machine-readable form, so the fact can be found,
// diffed and eventually checked by tooling rather than reread.

// paramContractRe matches an asserted preconditon: something the caller must
// have done before this call, which the signature does not enforce.
//
// Deliberately narrow. "must be non-empty" is a validation the function can do
// itself and usually does; "must already have passed X" names work done in
// another function whose result is carried by convention alone. Only the
// second is a missing type.
var paramContractRe = regexp.MustCompile(`(?i)\b(` +
	`MUST already|must have (already )?passed|already (been )?validated|already validated|` +
	`pre-?validated|already escaped|already sanitized|already normalized|` +
	`caller must have|callers? must ensure|must be called (only )?(after|with)|` +
	`assumed to be (valid|safe|sanitized)|is trusted|only ever called with|` +
	`must be (pre-?validated|sanitized|escaped|normalized) (by|before)|` +
	`(was|were) already resolved through` +
	`)\b`)

// primitiveTypes are the types that carry no invariant. A parameter of one of
// these, described by a precondition, is the signal.
var primitiveTypes = map[string]bool{
	"string": true, "int": true, "int64": true, "int32": true,
	"uint": true, "uint64": true, "byte": true, "rune": true,
	"float64": true, "bool": true,
}

// ContractFinding is one precondition asserted in prose about a parameter the
// type system does not constrain.
type ContractFinding struct {
	C         Comment
	Params    []string // primitive-typed params named in the assertion
	Assertion string   // the sentence carrying the precondition
}

// FindParamContracts reports doc comments that state a precondition about a
// primitively-typed parameter.
//
// The check is deliberately conservative: the assertion must name a parameter
// that actually exists on the function AND that parameter must have a type
// with no room for the invariant. A precondition about an already-structured
// type is not actionable — the type is there, the comment is explaining it.
func FindParamContracts(comments []Comment) []ContractFinding {
	var out []ContractFinding
	for _, c := range comments {
		if c.Level != LevelExportedDecl && c.Level != LevelUnexportedDecl {
			continue
		}
		if len(c.Params) == 0 {
			continue
		}
		sent := findAssertion(c.Text)
		if sent == "" {
			continue
		}
		var named []string
		for _, p := range c.Params {
			if !primitiveTypes[p.Type] {
				continue
			}
			if mentionsParam(sent, p.Name) {
				named = append(named, p.Name+" "+p.Type)
			}
		}
		if len(named) == 0 {
			continue
		}
		sort.Strings(named)
		out = append(out, ContractFinding{C: c, Params: named, Assertion: sent})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].C.File != out[j].C.File {
			return out[i].C.File < out[j].C.File
		}
		return out[i].C.LineNo < out[j].C.LineNo
	})
	return out
}

// findAssertion returns the sentence carrying the precondition, or "".
func findAssertion(text string) string {
	flat := strings.Join(strings.Fields(text), " ")
	for _, s := range splitSentences(flat) {
		if paramContractRe.MatchString(s) {
			return s
		}
	}
	return ""
}

var sentenceRe = regexp.MustCompile(`(?:[.!?])\s+`)

func splitSentences(s string) []string {
	return sentenceRe.Split(s, -1)
}

// mentionsParam reports whether a sentence names the parameter. Matching is on
// a word boundary and case-insensitively on the first letter only, so a
// sentence opening with "RepoPath must..." still matches repoPath.
func mentionsParam(sentence, name string) bool {
	if name == "" || name == "_" {
		return false
	}
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
	if re.MatchString(sentence) {
		return true
	}
	if len(name) > 1 {
		alt := strings.ToUpper(name[:1]) + name[1:]
		return regexp.MustCompile(`\b` + regexp.QuoteMeta(alt) + `\b`).MatchString(sentence)
	}
	return false
}

func (f ContractFinding) String(rel func(string) string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s:%d: [param-contract] %s asserts a precondition the signature does not enforce — a named type would make the unvalidated call fail to compile.\n",
		rel(f.C.File), f.C.LineNo, f.C.Ident)
	fmt.Fprintf(&sb, "    ├ unconstrained: %s\n", strings.Join(f.Params, ", "))
	fmt.Fprintf(&sb, "    │ %s\n", wrapExcerpt(f.Assertion))
	return sb.String()
}

// ---------------------------------------------------------------------------
// nil-contract

// nilFormRe is the standard form. A tag, a colon, then free prose: the tag is
// what tooling reads, the prose is why — which varies per call site and is the
// part worth keeping.
//
//	Nil: rejected — NewDeclarative returns an error.
//	Nil: accepted — disables scan/transform, native MIME validation only.
//	Nil: never returned.
var nilFormRe = regexp.MustCompile(`(?m)^\s*Nil: (rejected|accepted|never returned)\b`)

// nilProseRe matches the ad-hoc phrasings the standard form replaces.
// Bare "non-nil" is excluded deliberately: it is overwhelmingly used to draw
// the nil-vs-empty-slice distinction ("`read: []` decodes to a non-nil empty
// slice"), which is a description of decoding behaviour, not a contract about
// what a caller may pass. Only phrasings that state a REQUIREMENT or a
// documented nil MEANING are matched.
var nilProseRe = regexp.MustCompile(`(?i)(` +
	`\bmust not be nil\b|\bmust be non-nil\b|\bmust(?: all)? be non-nil\b|` +
	`\bnever nil\b|\bmay be nil\b|\bcan be nil\b|\bis nil-safe\b|` +
	`\bor nil when\b|\bor nil if\b|\bnil means\b|\bnil disables\b|` +
	`\bnil skips\b|\bnil is treated\b|\breturns nil when\b|\breturns nil if\b` +
	`)`)

// NilFinding is one nil contract stated in ad-hoc prose.
type NilFinding struct {
	C      Comment
	Phrase string
	Kind   string // suggested standard tag
}

// FindNilContracts reports nil behaviour described in prose rather than the
// standard form. Comments already carrying a `Nil:` tag are skipped, so the
// rule converges: fixing one removes it permanently.
func FindNilContracts(comments []Comment) []NilFinding {
	var out []NilFinding
	for _, c := range comments {
		if c.Level == LevelBody || c.Level == LevelPackage {
			continue
		}
		if nilFormRe.MatchString(c.Text) {
			continue
		}
		if errorOnly(c.Results) {
			continue
		}
		flat := strings.Join(strings.Fields(c.Text), " ")
		m := nilProseRe.FindString(flat)
		if m == "" {
			continue
		}
		out = append(out, NilFinding{C: c, Phrase: m, Kind: suggestNilKind(strings.ToLower(m))})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].C.File != out[j].C.File {
			return out[i].C.File < out[j].C.File
		}
		return out[i].C.LineNo < out[j].C.LineNo
	})
	return out
}

func suggestNilKind(phrase string) string {
	switch {
	case strings.Contains(phrase, "must not be nil"),
		strings.Contains(phrase, "must be non-nil"),
		strings.Contains(phrase, "must all be non-nil"):
		return "rejected"
	case strings.Contains(phrase, "never nil"):
		return "never returned"
	default:
		return "accepted"
	}
}

func (f NilFinding) String(rel func(string) string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s:%d: [nil-contract] %s states nil behaviour as prose (%q). Go cannot express this in a type, so use the standard form.\n",
		rel(f.C.File), f.C.LineNo, label(f.C), f.Phrase)
	fmt.Fprintf(&sb, "    ├ suggested: Nil: %s — <why>\n", f.Kind)
	fmt.Fprintf(&sb, "    │ %s\n", wrapExcerpt(f.C.Text))
	return sb.String()
}

// resultsOf returns the leaf type names of a function's results.
func resultsOf(fn *ast.FuncDecl) []string {
	if fn.Type == nil || fn.Type.Results == nil {
		return nil
	}
	var out []string
	for _, f := range fn.Type.Results.List {
		t := leafTypeName(f.Type)
		n := len(f.Names)
		if n == 0 {
			n = 1
		}
		for range n {
			out = append(out, t)
		}
	}
	return out
}

// errorOnly reports whether a function's only result is error. "Returns nil if
// the config is valid" on such a function is Go's universal error idiom, not a
// statement about nil-ness — it was the entire false-positive class in a
// 12-finding spot check (4/12).
func errorOnly(results []string) bool {
	return len(results) == 1 && results[0] == "error"
}

// paramsOf returns the parameter names and type names of a function
// declaration. Only the leaf type name is kept: a *T, []T and T all fail to
// carry an invariant about T's contents in the same way.
func paramsOf(fn *ast.FuncDecl) []Param {
	if fn.Type == nil || fn.Type.Params == nil {
		return nil
	}
	var out []Param
	for _, f := range fn.Type.Params.List {
		t := leafTypeName(f.Type)
		for _, n := range f.Names {
			out = append(out, Param{Name: n.Name, Type: t})
		}
	}
	return out
}

// leafTypeName reduces a type expression to its base identifier.
func leafTypeName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.StarExpr:
		return leafTypeName(v.X)
	case *ast.ArrayType:
		// []byte is as unconstrained as string; [] of a named type is not.
		return leafTypeName(v.Elt)
	case *ast.SelectorExpr:
		return v.Sel.Name
	}
	return ""
}
