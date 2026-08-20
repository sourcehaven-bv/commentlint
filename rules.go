package main

import (
	"fmt"
	"sort"
	"strings"
)

type Config struct {
	MaxBodyLines  int
	MaxUnexpLines int
	ReachRatio    float64 // fraction of informative tokens outside scope to flag
	MinInform     float64 // minimum added-information score for a doc comment
	RequirePrefix bool
	Rules         map[string]bool
}

func DefaultConfig() Config {
	return Config{
		MaxBodyLines:  3,
		MaxUnexpLines: 5,
		ReachRatio:    0.5,
		MinInform:     0.15,
		RequirePrefix: false,
		Rules: map[string]bool{
			"commented-code": true,
			"restatement":    true,
			"scope-reach":    true,
			"too-long":       true,
			"untyped":        true,
			"no-removal":     true,
			// Opt-in: duplication is a cross-comment rule with its own
			// output shape (it prints both texts), so it runs instead of
			// the per-comment rules rather than alongside them.
			"duplication": false,
			// Opt-in for the same reason as duplication: their output
			// shape differs from the per-comment rules.
			"param-contract": false,
			"nil-contract":   false,
		},
	}
}

// minCorpusScopes is the smallest corpus where IDF separates rare identifiers
// from ordinary words. Below it, scope-reach stays silent rather than guessing.
const minCorpusScopes = 60

type Analyzer struct {
	cfg   Config
	vocab *Vocabulary
}

func (a *Analyzer) Check(c Comment) []Finding {
	if c.IsDirective || strings.TrimSpace(c.Text) == "" {
		return nil
	}
	var out []Finding
	add := func(rule, msg string, score float64, outside []string) {
		if !a.cfg.Rules[rule] {
			return
		}
		out = append(out, Finding{
			File: c.File, Line: c.LineNo, Level: c.Level, Rule: rule,
			Score: score, Message: msg, Excerpt: excerpt(c.Text), Outside: outside,
		})
	}

	if a.cfg.Rules["commented-code"] && looksLikeCode(c.Text) {
		add("commented-code", "commented-out code — delete it; git remembers.", 1.0, nil)
		return out
	}

	switch c.Level {
	case LevelPackage:
		// Free-form by design. Only reach is checked, and loosely: a package
		// doc may legitimately name its own exported surface.
		return out

	case LevelExportedDecl:
		if s, ok := a.restates(c); ok {
			add("restatement", fmt.Sprintf(
				"doc for %q restates the name and adds nothing. Say what a caller needs: inputs, invariants, edge behaviour.",
				c.Ident), s, nil)
		}

	case LevelUnexportedDecl:
		if s, ok := a.restates(c); ok {
			add("restatement", fmt.Sprintf(
				"doc for %q restates the name. If it needs explaining, the WHY is why it exists separately — otherwise delete.",
				c.Ident), s, nil)
		}
		if c.Lines > a.cfg.MaxUnexpLines {
			add("too-long", fmt.Sprintf(
				"%d lines on an unexported decl. Long comments here usually describe the package or the system, not this function.",
				c.Lines), float64(c.Lines)/10, nil)
		}
		if out, tok := a.reaches(c); out {
			add("scope-reach", "doc names things outside this function's scope — that belongs at package level or in the README.", 0.7, tok)
		}

	case LevelBody:
		if c.Lines > a.cfg.MaxBodyLines {
			add("too-long", fmt.Sprintf(
				"%d-line comment inside a function body. A statement-local WHY is short; this length signals it is explaining the system, not the line.",
				c.Lines), float64(c.Lines)/10, nil)
		}
		if reach, tok := a.reaches(c); reach {
			add("scope-reach", "comment names things not in scope here — it is explaining another part of the system.", 0.8, tok)
		}
		if a.cfg.RequirePrefix && !typedRe.MatchString(strings.TrimSpace(c.Text)) {
			add("untyped", bodyPrefixHelp(), 0.5, nil)
		}
		if m := whyKindRe.FindStringSubmatch(strings.TrimSpace(c.Text)); m != nil && m[1] == "workaround" {
			if !removalRe.MatchString(c.Text) {
				add("no-removal", "Why(workaround) must state a removal condition: an issue, a version, or a trigger.", 0.6, nil)
			}
		}
	}
	return out
}

// restates reports whether a doc comment adds no information beyond the
// identifier's own name, weighted by how rare the added words are.
func (a *Analyzer) restates(c Comment) (float64, bool) {
	if c.Ident == "" {
		return 0, false
	}
	identToks := map[string]bool{}
	for _, t := range Tokenize(c.Ident) {
		identToks[t] = true
	}
	var added, total float64
	for _, t := range Tokenize(c.Text) {
		idf := a.vocab.IDF(t)
		total += idf
		if !identToks[t] {
			added += idf
		}
	}
	if total == 0 {
		return 0, false
	}
	ratio := added / total
	if ratio < a.cfg.MinInform {
		return 1 - ratio, true
	}
	return 0, false
}

// reaches reports whether the comment's informative vocabulary names things
// that are not in scope at this position. A token only counts as evidence if
// it names something the corpus actually declares somewhere — otherwise
// ordinary English words dominate the score on small corpora.
func (a *Analyzer) reaches(c Comment) (bool, []string) {
	if a.vocab.scopes < minCorpusScopes {
		return false, nil
	}

	inScope := map[string]bool{}
	for _, id := range c.Scope {
		for _, t := range Tokenize(id) {
			inScope[t] = true
		}
	}
	for _, t := range Tokenize(c.Ident) {
		inScope[t] = true
	}

	cutoff := a.vocab.MaxIDF() * 0.35
	var informative, outside float64
	seen := map[string]bool{}
	var names []string
	for _, raw := range codeCandidates(c.Text) {
		t := stem(strings.ToLower(raw))
		df := a.vocab.docFreq[t]
		// Must name something declared elsewhere in the corpus. A token the
		// corpus never declares is prose, not a reference to another scope.
		if df == 0 {
			continue
		}
		idf := a.vocab.IDF(t)
		if idf < cutoff {
			continue
		}
		informative += idf
		if !inScope[t] {
			outside += idf
			if !seen[t] {
				seen[t] = true
				names = append(names, raw)
			}
		}
	}
	if informative < cutoff*2.5 {
		return false, nil
	}
	if outside/informative >= a.cfg.ReachRatio {
		sort.Strings(names)
		if len(names) > 6 {
			names = names[:6]
		}
		return true, names
	}
	return false, nil
}

// codeCandidates returns only the words that are unambiguously naming code:
// camelCase, snake_case, backticked spans, dotted paths, and ALLCAPS. Bare
// lowercase English is excluded entirely — on a large Go corpus almost every
// common word ("show", "first", "back") also appears inside some identifier,
// so membership in the vocabulary is not evidence of anything.
func codeCandidates(text string) []string {
	var out []string
	for _, span := range backtickRe.FindAllStringSubmatch(text, -1) {
		out = append(out, splitIdent(span[1])...)
	}
	stripped := backtickRe.ReplaceAllString(text, " ")
	for _, w := range wordRe.FindAllString(stripped, -1) {
		if len(w) < 3 || !CodeLike(w) {
			continue
		}
		for _, part := range splitIdent(w) {
			if len(part) >= 3 {
				out = append(out, part)
			}
		}
	}
	return out
}

func looksLikeCode(text string) bool {
	lines := strings.Split(text, "\n")
	var code, real int
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		real++
		if strings.HasSuffix(l, ";") || strings.HasSuffix(l, "{") || strings.HasSuffix(l, "}") ||
			codeFragRe.MatchString(l) && strings.ContainsAny(l, "(){}=") {
			code++
		}
	}
	return real > 0 && code == real && real >= 2
}

func bodyPrefixHelp() string {
	return strings.Join([]string{
		"body comment must declare its type — explain WHY, not WHAT.",
		"    Why: <why this and not the obvious form>",
		"    Why(perf): <the measurement that justifies it>",
		"    Why(workaround): <upstream bug + removal condition>",
		"    Ref: <url or spec section>",
		"  If none fit, the comment is probably restating the code — delete it.",
	}, "\n  ")
}

func excerpt(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 90 {
		s = s[:87] + "..."
	}
	return s
}
