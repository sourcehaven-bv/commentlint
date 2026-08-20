package main

import (
	"math"
	"regexp"
	"strings"
)

// Vocabulary is the corpus-wide identifier vocabulary, used to compute how
// rare a token is. Rare tokens carry information; ubiquitous ones do not.
type Vocabulary struct {
	docFreq map[string]int // token -> number of scopes it appears in
	scopes  int
}

func NewVocabulary() *Vocabulary {
	return &Vocabulary{docFreq: map[string]int{}}
}

// Observe records one scope's identifier tokens. Call once per function.
func (v *Vocabulary) Observe(tokens []string) {
	v.scopes++
	seen := map[string]bool{}
	for _, t := range tokens {
		if seen[t] {
			continue
		}
		seen[t] = true
		v.docFreq[t]++
	}
}

// IDF returns the inverse document frequency of a token. An unseen token gets
// the maximum score: it names something that exists nowhere in this corpus.
func (v *Vocabulary) IDF(tok string) float64 {
	if v.scopes == 0 {
		return 0
	}
	df := v.docFreq[tok]
	return math.Log(float64(v.scopes+1) / float64(df+1))
}

// MaxIDF is the score of a token appearing in exactly one scope.
func (v *Vocabulary) MaxIDF() float64 {
	if v.scopes == 0 {
		return 0
	}
	return math.Log(float64(v.scopes + 1))
}

var (
	wordRe     = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_]*`)
	camelRe    = regexp.MustCompile(`[A-Z]+[a-z0-9]*|[a-z0-9]+`)
	urlRe      = regexp.MustCompile(`https?://\S+`)
	backtickRe = regexp.MustCompile("`([^`]+)`")
	stopWord   = map[string]bool{}
)

func init() {
	// English function words plus the verbs that dominate code prose. These
	// carry no scope information either way, so they are excluded before any
	// ratio is computed.
	for _, w := range strings.Fields(`
		a an the this that these those it its is are was were be been being
		and or but not no if then else when while for to from of in on at by
		with as we you they he she them us our your their i
		do does did done can could should would may might must will shall
		have has had here there where which who whom what why how
		so than too very just only also both each few more most other some such
		own same s t don now
		use used uses using make makes made get gets got set sets
		return returns returned call calls called run runs
		need needs needed want wants keep keeps
		note todo fixme xxx hack
	`) {
		stopWord[w] = true
	}
}

// Tokenize splits text into lowercase word tokens, splitting camelCase and
// snake_case so that identifiers and prose share a vocabulary. Stop words and
// single characters are dropped.
func Tokenize(s string) []string {
	s = urlRe.ReplaceAllString(s, " ")
	var out []string
	for _, w := range wordRe.FindAllString(s, -1) {
		for _, part := range splitIdent(w) {
			part = strings.ToLower(part)
			if len(part) < 2 || stopWord[part] {
				continue
			}
			out = append(out, stem(part))
		}
	}
	return out
}

func splitIdent(w string) []string {
	var out []string
	for _, seg := range strings.Split(w, "_") {
		if seg == "" {
			continue
		}
		out = append(out, camelRe.FindAllString(seg, -1)...)
	}
	return out
}

// stem is a deliberately crude suffix stripper. It exists to make "merges"
// match "merge", not to be linguistically correct; anything cleverer would
// add a dependency for no measurable gain here.
func stem(w string) string {
	switch {
	case strings.HasSuffix(w, "ies") && len(w) > 4:
		return w[:len(w)-3] + "y"
	case strings.HasSuffix(w, "sses"):
		return w[:len(w)-2]
	case strings.HasSuffix(w, "ing") && len(w) > 5:
		return strings.TrimSuffix(w[:len(w)-3], "e")
	case strings.HasSuffix(w, "ed") && len(w) > 4:
		return strings.TrimSuffix(w[:len(w)-2], "e")
	case strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && len(w) > 3:
		return w[:len(w)-1]
	}
	return w
}

// CodeLike reports whether a token is unambiguously naming code: it has an
// underscore, or an internal capital (fooBar, HTTPClient, Config.Path). A
// leading capital alone does not count — that is just the start of a sentence.
func CodeLike(raw string) bool {
	if strings.Contains(raw, "_") {
		return true
	}
	if len(raw) < 2 {
		return false
	}
	inner := raw[1:]
	if strings.ToLower(inner) == inner {
		return false
	}
	// All-caps acronyms (JWT, ACL) are identifiers; mixed case is camelCase.
	return true
}
