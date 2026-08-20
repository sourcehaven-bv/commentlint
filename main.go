// Command commentlint checks Go comments against a scope discipline: a comment
// may only talk about the scope it is attached to. It reports comments that
// restate their identifier, reach outside their scope, or are long enough that
// they are describing the system rather than the code they sit on.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	cfg := DefaultConfig()
	var (
		rank     = flag.Bool("rank", false, "rank all findings worst-first instead of failing")
		top      = flag.Int("top", 40, "with -rank, show this many")
		tests    = flag.Bool("tests", false, "include _test.go files")
		only     = flag.String("rules", "", "comma-separated rules to enable")
		verbose  = flag.Bool("v", false, "show per-rule counts and corpus stats")
		strictPx = flag.Bool("require-prefix", false, "require Why:/Ref: prefixes on body comments")
	)
	flag.IntVar(&cfg.MaxBodyLines, "max-body-lines", cfg.MaxBodyLines, "max lines for a comment inside a function body")
	flag.IntVar(&cfg.MaxUnexpLines, "max-decl-lines", cfg.MaxUnexpLines, "max lines for an unexported decl doc")
	flag.Float64Var(&cfg.ReachRatio, "reach-ratio", cfg.ReachRatio, "flag when this fraction of informative tokens is out of scope")
	flag.Parse()

	cfg.RequirePrefix = *strictPx
	if *only != "" {
		for k := range cfg.Rules {
			cfg.Rules[k] = false
		}
		for _, r := range strings.Split(*only, ",") {
			cfg.Rules[strings.TrimSpace(r)] = true
		}
	}

	roots := flag.Args()
	if len(roots) == 0 {
		roots = []string{"."}
	}

	files, err := goFiles(roots, *tests)
	if err != nil {
		fmt.Fprintln(os.Stderr, "commentlint:", err)
		os.Exit(2)
	}
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "commentlint: no Go files found")
		os.Exit(2)
	}

	// Pass 1: build the corpus vocabulary from every function's identifiers,
	// so IDF reflects this repo's naming rather than English generally.
	fset := token.NewFileSet()
	vocab := NewVocabulary()
	type parsed struct {
		file *ast.File
		name string
	}
	var asts []parsed
	for _, fn := range files {
		f, err := parser.ParseFile(fset, fn, nil, parser.ParseComments)
		if err != nil {
			continue
		}
		asts = append(asts, parsed{f, fn})
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok {
				vocab.Observe(Tokenize(strings.Join(funcScope(fd), " ")))
			}
		}
	}

	// Pass 2: check every comment against the scope it is attached to.
	a := &Analyzer{cfg: cfg, vocab: vocab}
	var findings []Finding
	total := 0
	for _, p := range asts {
		for _, c := range collectComments(fset, p.file, p.name) {
			if !c.IsDirective {
				total++
			}
			findings = append(findings, a.Check(c)...)
		}
	}

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Score != findings[j].Score {
			return findings[i].Score > findings[j].Score
		}
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		return findings[i].Line < findings[j].Line
	})

	if *verbose {
		byRule := map[string]int{}
		for _, f := range findings {
			byRule[f.Rule]++
		}
		fmt.Printf("corpus: %d files, %d functions, %d vocab terms, %d comments\n",
			len(asts), vocab.scopes, len(vocab.docFreq), total)
		keys := make([]string, 0, len(byRule))
		for k := range byRule {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("  %-16s %d\n", k, byRule[k])
		}
		fmt.Println()
	}

	shown := findings
	if *rank && len(shown) > *top {
		shown = shown[:*top]
	}
	for _, f := range shown {
		rel := f.File
		if wd, err := os.Getwd(); err == nil {
			if r, err := filepath.Rel(wd, f.File); err == nil && !strings.HasPrefix(r, "..") {
				rel = r
			}
		}
		fmt.Printf("%s:%d: [%s/%s] %s\n", rel, f.Line, f.Level, f.Rule, f.Message)
		fmt.Printf("    │ %s\n", f.Excerpt)
		if len(f.Outside) > 0 {
			fmt.Printf("    └ out-of-scope terms: %s\n", strings.Join(f.Outside, ", "))
		}
	}

	if len(findings) > 0 {
		pct := 100 * float64(len(findings)) / float64(max(total, 1))
		fmt.Printf("\n%d findings across %d comments (%.1f%%)\n", len(findings), total, pct)
		if !*rank {
			os.Exit(1)
		}
	} else {
		fmt.Printf("no findings across %d comments\n", total)
	}
}

func goFiles(roots []string, withTests bool) ([]string, error) {
	var out []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				base := d.Name()
				if base == "vendor" || base == "testdata" || base == "node_modules" ||
					(strings.HasPrefix(base, ".") && base != "." && base != "..") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			if !withTests && strings.HasSuffix(path, "_test.go") {
				return nil
			}
			abs, err := filepath.Abs(path)
			if err != nil {
				return nil
			}
			out = append(out, abs)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
