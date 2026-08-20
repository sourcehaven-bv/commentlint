package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Duplication finds prose that has been explained more than once. It is a
// cross-comment rule: every other rule judges a comment against its own scope,
// this one judges it against the rest of the corpus.
//
// The motivating case is a fact about a shared type restated at each of its
// call sites. Three sibling predicates that each re-derive "this ACL
// implementation makes the read gate fail open" is one fact stored three
// times, and it will be corrected in one place and stale in two.
//
// Length is NOT the signal. A long comment that says something once is fine —
// the worst offender on the corpus this was built against is 28 lines of
// verified security limitation on a one-line function. What length correlates
// with is duplication, which is why length-based rules appear to work and then
// mostly flag comments that earned their size.

// shingleK is the n-gram width. Six words is long enough that a collision
// means shared phrasing rather than shared subject matter, and short enough to
// survive the light editing that copied prose usually receives.
const shingleK = 6

// minParaWords is the shortest paragraph worth comparing. Below this a
// paragraph yields too few shingles for the overlap ratio to mean anything.
const minParaWords = 12

// DupConfig tunes the duplication rule.
type DupConfig struct {
	// MinOverlap is the fraction of a paragraph's shingles that must appear
	// in another comment before it is reported.
	MinOverlap float64
	// MaxSites caps how many comments may share a paragraph before it is
	// treated as shared vocabulary rather than duplication.
	MaxSites int
}

func DefaultDupConfig() DupConfig {
	return DupConfig{MinOverlap: 0.30, MaxSites: 8}
}

// DupFinding is one paragraph duplicated across two comments.
type DupFinding struct {
	A, B    Comment
	AText   string
	BText   string
	Shared  int
	Overlap float64
	Score   float64
}

var paraSplitRe = regexp.MustCompile(`\n\s*\n`)

// paragraph is one comparable unit of prose with its shingle set.
type paragraph struct {
	owner    int
	text     string
	shingles map[string]bool
}

// FindDuplication reports paragraphs that appear in more than one comment.
//
// Comments are compared pairwise through a shingle index rather than directly:
// the index is what keeps this near-linear in corpus size instead of O(n²) on
// the ~10k comments a mid-sized repo carries.
func FindDuplication(comments []Comment, cfg DupConfig) []DupFinding {
	paras := collectParagraphs(comments)

	index := map[string][]int{}
	for i, p := range paras {
		for sh := range p.shingles {
			index[sh] = append(index[sh], i)
		}
	}

	// For each paragraph, find the other paragraph it overlaps most.
	type pair struct{ a, b int }
	best := map[pair]DupFinding{}
	for _, p := range paras {
		if len(p.shingles) == 0 {
			continue
		}
		counts := map[int]int{}
		for sh := range p.shingles {
			owners := index[sh]
			// A phrase shared by many comments is a house idiom, not a
			// copy: "returns store.ErrNotFound if the entity does not
			// exist" is the interface contract, restated legitimately by
			// every backend that implements it.
			if len(owners) > cfg.MaxSites {
				continue
			}
			for _, j := range owners {
				if paras[j].owner != p.owner {
					counts[j]++
				}
			}
		}
		for j, n := range counts {
			ov := float64(n) / float64(len(p.shingles))
			if ov < cfg.MinOverlap {
				continue
			}
			a, b := comments[p.owner], comments[paras[j].owner]
			key := pair{p.owner, paras[j].owner}
			if key.a > key.b {
				key.a, key.b = key.b, key.a
			}
			if prev, ok := best[key]; ok && prev.Shared >= n {
				continue
			}
			fa, fb := a, b
			ta, tb := p.text, paras[j].text
			if key.a != p.owner {
				fa, fb = b, a
				ta, tb = paras[j].text, p.text
			}
			best[key] = DupFinding{
				A: fa, B: fb, AText: ta, BText: tb,
				Shared: n, Overlap: ov,
				// Rank by duplicated VOLUME, not fraction. Fraction
				// promotes small twin helpers whose whole doc is two
				// shared sentences; volume promotes the cases where
				// hoisting the fact deletes the most text.
				Score: float64(n),
			}
		}
	}

	out := make([]DupFinding, 0, len(best))
	for _, f := range best {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].A.File != out[j].A.File {
			return out[i].A.File < out[j].A.File
		}
		return out[i].A.LineNo < out[j].A.LineNo
	})
	return out
}

func collectParagraphs(comments []Comment) []paragraph {
	var out []paragraph
	for i, c := range comments {
		if c.IsDirective {
			continue
		}
		for _, raw := range paraSplitRe.Split(c.Text, -1) {
			toks := normalizeProse(raw)
			if len(toks) < minParaWords {
				continue
			}
			sh := shingleSet(toks)
			if len(sh) == 0 {
				continue
			}
			out = append(out, paragraph{owner: i, text: raw, shingles: sh})
		}
	}
	return out
}

// normalizeProse reduces a paragraph to comparable word tokens. Doc links
// ([acl.ReadOnlyACL]) and punctuation are dropped so that the same sentence
// survives being re-linked or re-punctuated between copies.
func normalizeProse(s string) []string {
	s = strings.ToLower(s)
	s = docLinkRe.ReplaceAllString(s, " ")
	s = urlRe.ReplaceAllString(s, " ")
	var out []string
	for _, w := range wordRe.FindAllString(s, -1) {
		if len(w) <= 2 {
			continue
		}
		out = append(out, w)
	}
	return out
}

var docLinkRe = regexp.MustCompile(`\[[^\]]*\]`)

func shingleSet(toks []string) map[string]bool {
	out := map[string]bool{}
	if len(toks) < shingleK {
		return out
	}
	for i := 0; i+shingleK <= len(toks); i++ {
		out[strings.Join(toks[i:i+shingleK], " ")] = true
	}
	return out
}

// Cluster collapses a transitive group of duplicates into one finding. Three
// commands sharing one paragraph produce three pairs, which reads as three
// problems when it is one fact stored three times; the operator wants a single
// entry naming every site.
func Cluster(findings []DupFinding, sameName bool) []DupCluster {
	parent := map[string]string{}
	var find func(string) string
	find = func(k string) string {
		if parent[k] == "" || parent[k] == k {
			parent[k] = k
			return k
		}
		parent[k] = find(parent[k])
		return parent[k]
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}

	key := func(c Comment) string { return fmt.Sprintf("%s:%d", c.File, c.LineNo) }
	kept := make([]DupFinding, 0, len(findings))
	for _, f := range findings {
		if !sameName && SameNameSiblings(f.A, f.B) {
			continue
		}
		kept = append(kept, f)
		union(key(f.A), key(f.B))
	}

	byRoot := map[string]*DupCluster{}
	var order []string
	seen := map[string]bool{}
	for _, f := range kept {
		r := find(key(f.A))
		c, ok := byRoot[r]
		if !ok {
			c = &DupCluster{}
			byRoot[r] = c
			order = append(order, r)
		}
		if c.Best.Shared < f.Shared {
			c.Best = f
		}
		c.Shared += f.Shared
		for _, m := range []Comment{f.A, f.B} {
			if k := key(m); !seen[r+"|"+k] {
				seen[r+"|"+k] = true
				c.Sites = append(c.Sites, m)
			}
		}
	}

	out := make([]DupCluster, 0, len(order))
	for _, r := range order {
		c := byRoot[r]
		sort.Slice(c.Sites, func(i, j int) bool {
			if c.Sites[i].File != c.Sites[j].File {
				return c.Sites[i].File < c.Sites[j].File
			}
			return c.Sites[i].LineNo < c.Sites[j].LineNo
		})
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Shared != out[j].Shared {
			return out[i].Shared > out[j].Shared
		}
		return out[i].Best.A.File < out[j].Best.A.File
	})
	return out
}

// DupCluster is one fact and every comment that restates it.
type DupCluster struct {
	Sites  []Comment
	Best   DupFinding
	Shared int
}

func (c DupCluster) String(rel func(string) string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s:%d: [duplication] this paragraph is restated in %d places (%.0f%% shared) — hoist the fact to the type or package they all cite.\n",
		rel(c.Best.A.File), c.Best.A.LineNo, len(c.Sites), c.Best.Overlap*100)
	for _, s := range c.Sites {
		fmt.Fprintf(&sb, "    ├ %s:%d %s\n", rel(s.File), s.LineNo, label(s))
	}
	fmt.Fprintf(&sb, "    │ A %s: %s\n", label(c.Best.A), wrapExcerpt(c.Best.AText))
	fmt.Fprintf(&sb, "    │ B %s: %s\n", label(c.Best.B), wrapExcerpt(c.Best.BText))
	return sb.String()
}

// SameNameSiblings reports whether two comments document identically-named
// declarations. Interface implementations across backend packages legitimately
// repeat their contract, so those are suppressed rather than reported.
func SameNameSiblings(a, b Comment) bool {
	return a.Ident != "" && a.Ident == b.Ident
}

func label(c Comment) string {
	if c.Ident == "" {
		return "(" + c.Level.String() + ")"
	}
	return c.Ident
}

// wrapExcerpt shows enough of both paragraphs to judge the finding without
// opening either file. The whole value of this rule is that the two texts sit
// side by side; truncating to one line would throw that away.
func wrapExcerpt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	// Truncate on RUNES, not bytes: this corpus is full of em dashes and
	// arrows, and slicing mid-sequence emits invalid UTF-8 that breaks any
	// tool consuming the output.
	const width = 200
	r := []rune(s)
	if len(r) > width {
		s = string(r[:width-3]) + "..."
	}
	return s
}

func (f DupFinding) String(rel func(string) string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s:%d: [duplication] this paragraph is %.0f%% shared with %s:%d — hoist the fact to the type or package both cite.\n",
		rel(f.A.File), f.A.LineNo, f.Overlap*100, rel(f.B.File), f.B.LineNo)
	fmt.Fprintf(&sb, "    │ A %s: %s\n", label(f.A), wrapExcerpt(f.AText))
	fmt.Fprintf(&sb, "    │ B %s: %s\n", label(f.B), wrapExcerpt(f.BText))
	return sb.String()
}
