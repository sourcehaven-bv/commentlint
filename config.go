package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// File config. Every rule here is a heuristic over prose, so false positives
// are a permanent fact rather than a bug to be fixed once. Two escape hatches,
// deliberately different in blast radius:
//
//   - an inline `//commentlint:ignore <rule>` on the flagged declaration,
//     which travels with the code and is reviewed in the diff that adds it;
//   - a `.commentlint.yml` at the repo root, for whole paths and for the
//     project-wide vocabulary (shared acronyms, house idioms).
//
// Inline is preferred for one-off judgements: it sits where the reader is and
// cannot silently widen. The file is for policy that would otherwise be
// repeated at dozens of sites.
type FileConfig struct {
	// Exclude are path globs skipped entirely (matched against the
	// repo-relative path, and against each path segment, so "testdata"
	// excludes any directory of that name).
	Exclude []string
	// Rules enables or disables a rule wholesale.
	Rules map[string]bool
	// Ignore maps a rule name to path globs it does not apply to.
	Ignore map[string][]string
	// AllowPhrases are prose fragments that never count as a finding. Used
	// for house idioms a rule keeps mistaking for a defect.
	AllowPhrases []string
	// Ints holds the numeric tunables (max-body-lines, dup-overlap, ...) so
	// a project pins them once instead of in every CI invocation.
	Ints   map[string]int
	Floats map[string]float64

	allow []*regexp.Regexp
}

// LoadFileConfig reads .commentlint.yml from dir, walking up to the repo root.
// A missing file is not an error: the tool must work with no configuration.
//
// The format is an intentionally tiny subset of YAML — nested keys, lists and
// scalars, no anchors or flow style. Depending on a YAML library for a config
// this small would be the only dependency in the module.
func LoadFileConfig(start string) (*FileConfig, string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return &FileConfig{}, "", nil
	}
	for {
		for _, name := range []string{".commentlint.yml", ".commentlint.yaml"} {
			p := filepath.Join(dir, name)
			if b, err := os.ReadFile(p); err == nil {
				cfg, perr := parseFileConfig(string(b))
				if perr != nil {
					return nil, p, fmt.Errorf("%s: %w", p, perr)
				}
				return cfg, p, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return &FileConfig{}, "", nil
		}
		dir = parent
	}
}

// parseFileConfig reads the supported subset:
//
//	exclude:
//	  - "internal/legacy/**"
//	rules:
//	  too-long: false
//	ignore:
//	  nil-contract:
//	    - "internal/store/**"
//	allow-phrases:
//	  - "non-nil empty slice"
//	settings:
//	  max-body-lines: 6
//	  dup-overlap: 0.35
func parseFileConfig(src string) (*FileConfig, error) {
	cfg := &FileConfig{
		Rules:  map[string]bool{},
		Ignore: map[string][]string{},
		Ints:   map[string]int{},
		Floats: map[string]float64{},
	}
	var section, subkey string
	for n, raw := range strings.Split(src, "\n") {
		line := strings.TrimRight(raw, " \t\r")
		if i := strings.Index(line, " #"); i >= 0 {
			line = line[:i]
		}
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		t := strings.TrimSpace(line)

		if indent == 0 {
			section = strings.TrimSuffix(t, ":")
			subkey = ""
			continue
		}
		if strings.HasPrefix(t, "- ") {
			v := unquote(strings.TrimSpace(t[2:]))
			switch section {
			case "exclude":
				cfg.Exclude = append(cfg.Exclude, v)
			case "allow-phrases":
				cfg.AllowPhrases = append(cfg.AllowPhrases, v)
			case "ignore":
				if subkey == "" {
					return nil, fmt.Errorf("line %d: list item under ignore with no rule name", n+1)
				}
				cfg.Ignore[subkey] = append(cfg.Ignore[subkey], v)
			}
			continue
		}
		k, v, ok := strings.Cut(t, ":")
		if !ok {
			return nil, fmt.Errorf("line %d: expected key: value", n+1)
		}
		k = strings.TrimSpace(k)
		v = unquote(strings.TrimSpace(v))
		if v == "" {
			subkey = k
			continue
		}
		switch section {
		case "rules":
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, fmt.Errorf("line %d: rules.%s must be true or false", n+1, k)
			}
			cfg.Rules[k] = b
		case "settings":
			if i, err := strconv.Atoi(v); err == nil {
				cfg.Ints[k] = i
			} else if f, err := strconv.ParseFloat(v, 64); err == nil {
				cfg.Floats[k] = f
			} else {
				return nil, fmt.Errorf("line %d: settings.%s must be a number", n+1, k)
			}
		}
	}
	for _, p := range cfg.AllowPhrases {
		cfg.allow = append(cfg.allow, regexp.MustCompile("(?i)"+regexp.QuoteMeta(p)))
	}
	return cfg, nil
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

// Excluded reports whether a path is skipped entirely.
func (c *FileConfig) Excluded(rel string) bool {
	return matchAnyGlob(c.Exclude, rel)
}

// Suppressed reports whether a rule is turned off for a path.
func (c *FileConfig) Suppressed(rule, rel string) bool {
	if enabled, ok := c.Rules[rule]; ok && !enabled {
		return true
	}
	return matchAnyGlob(c.Ignore[rule], rel)
}

// AllowedPhrase reports whether text contains a project-declared house idiom.
func (c *FileConfig) AllowedPhrase(text string) bool {
	for _, re := range c.allow {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// matchAnyGlob matches a repo-relative path against a glob. "**" matches any
// number of segments; a bare name matches any path segment, so "testdata"
// excludes every directory of that name without needing a leading "**/".
func matchAnyGlob(globs []string, rel string) bool {
	rel = filepath.ToSlash(rel)
	segs := strings.Split(rel, "/")
	for _, g := range globs {
		g = filepath.ToSlash(g)
		if strings.Contains(g, "**") {
			if matchDoubleStar(g, rel) {
				return true
			}
			continue
		}
		if ok, _ := filepath.Match(g, rel); ok {
			return true
		}
		if !strings.Contains(g, "/") {
			for _, s := range segs {
				if ok, _ := filepath.Match(g, s); ok {
					return true
				}
			}
		}
	}
	return false
}

func matchDoubleStar(glob, rel string) bool {
	prefix, suffix, _ := strings.Cut(glob, "**")
	prefix = strings.TrimSuffix(prefix, "/")
	suffix = strings.TrimPrefix(suffix, "/")
	if prefix != "" && !(rel == prefix || strings.HasPrefix(rel, prefix+"/")) {
		return false
	}
	if suffix == "" {
		return true
	}
	ok, _ := filepath.Match(suffix, filepath.Base(rel))
	return ok || strings.HasSuffix(rel, "/"+suffix)
}

// ignoreDirectiveRe matches an inline suppression. A bare
// `//commentlint:ignore` suppresses every rule on that declaration; naming
// rules narrows it. A reason after the rules is encouraged and unparsed.
var ignoreDirectiveRe = regexp.MustCompile(`//\s*commentlint:ignore(?:\s+([a-z-]+(?:\s*,\s*[a-z-]+)*))?`)

// InlineIgnores returns the rules suppressed by a directive in the comment, and
// whether a directive was present at all.
func InlineIgnores(text string) (map[string]bool, bool) {
	m := ignoreDirectiveRe.FindStringSubmatch(text)
	if m == nil {
		return nil, false
	}
	if strings.TrimSpace(m[1]) == "" {
		return nil, true // bare directive: all rules
	}
	out := map[string]bool{}
	for _, r := range strings.Split(m[1], ",") {
		out[strings.TrimSpace(r)] = true
	}
	return out, true
}

// applyFileConfig folds file settings into the effective configuration. Only
// keys the file actually names are touched, so unset keys keep their defaults.
func applyFileConfig(cfg *Config, dup *DupConfig, fc *FileConfig) {
	for k, v := range fc.Rules {
		cfg.Rules[k] = v
	}
	for k, v := range fc.Ints {
		switch k {
		case "max-body-lines":
			cfg.MaxBodyLines = v
		case "max-decl-lines":
			cfg.MaxUnexpLines = v
		case "dup-max-sites":
			dup.MaxSites = v
		}
	}
	for k, v := range fc.Floats {
		switch k {
		case "reach-ratio":
			cfg.ReachRatio = v
		case "dup-overlap":
			dup.MinOverlap = v
		}
	}
}

// suppressed reports whether a finding on this comment should be dropped:
// by path glob, by inline directive, or by a project-declared house idiom.
func suppressed(fc *FileConfig, rule, rel string, text string) bool {
	if fc.Suppressed(rule, rel) {
		return true
	}
	if fc.AllowedPhrase(text) {
		return true
	}
	if rules, ok := InlineIgnores(text); ok {
		return rules == nil || rules[rule]
	}
	return false
}

// writeFile is a test helper kept next to the config loader so tests can build
// a fixture tree without importing os in every file.
func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
