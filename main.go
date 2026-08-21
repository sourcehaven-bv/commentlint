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
	dupCfg := DefaultDupConfig()
	flag.Float64Var(&dupCfg.MinOverlap, "dup-overlap", dupCfg.MinOverlap,
		"fraction of a paragraph that must be shared to report duplication")
	flag.IntVar(&dupCfg.MaxSites, "dup-max-sites", dupCfg.MaxSites,
		"a phrase in more than this many comments is shared vocabulary, not duplication")
	sameName := flag.Bool("dup-same-name", false,
		"report duplication between identically-named decls (interface implementations)")
	flag.IntVar(&cfg.MaxBodyLines, "max-body-lines", cfg.MaxBodyLines, "max lines for a comment inside a function body")
	flag.IntVar(&cfg.MaxUnexpLines, "max-decl-lines", cfg.MaxUnexpLines, "max lines for an unexported decl doc")
	flag.Float64Var(&cfg.ReachRatio, "reach-ratio", cfg.ReachRatio, "flag when this fraction of informative tokens is out of scope")
	confPath := flag.String("config", "", "path to .commentlint.yml (default: nearest one, searching upward)")
	noConf := flag.Bool("no-config", false, "ignore any .commentlint.yml")
	flag.Parse()

	cfg.RequirePrefix = *strictPx

	roots := flag.Args()
	if len(roots) == 0 {
		roots = []string{"."}
	}

	fc := &FileConfig{}
	if !*noConf {
		var (
			used string
			err  error
		)
		if *confPath != "" {
			b, rerr := os.ReadFile(*confPath)
			if rerr != nil {
				fmt.Fprintln(os.Stderr, "commentlint:", rerr)
				os.Exit(2)
			}
			fc, err = parseFileConfig(string(b))
			used = *confPath
		} else {
			fc, used, err = LoadFileConfig(roots[0])
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "commentlint:", err)
			os.Exit(2)
		}
		if *verbose && used != "" {
			fmt.Printf("config: %s\n", used)
		}
	}
	applyFileConfig(&cfg, &dupCfg, fc)

	// -rules is applied last: an explicit flag always beats the file, so a
	// developer can run a single rule without editing shared config.
	if *only != "" {
		for k := range cfg.Rules {
			cfg.Rules[k] = false
		}
		for _, r := range strings.Split(*only, ",") {
			cfg.Rules[strings.TrimSpace(r)] = true
		}
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
	wd, _ := os.Getwd()
	a := &Analyzer{cfg: cfg, vocab: vocab}
	var findings []Finding
	var corpus []Comment
	total := 0
	for _, p := range asts {
		if fc.Excluded(relTo(wd, p.name)) {
			continue
		}
		for _, c := range collectComments(fset, p.file, p.name) {
			if !c.IsDirective {
				total++
			}
			corpus = append(corpus, c)
			for _, f := range a.Check(c) {
				if suppressed(fc, f.Rule, relTo(wd, f.File), c.Text+"\n"+c.Trailer) {
					continue
				}
				findings = append(findings, f)
			}
		}
	}

	rel := func(path string) string { return relTo(wd, path) }

	if cfg.Rules["doclink"] {
		// Grouped by directory: LookupSym is package-scoped, so resolving a
		// reference against the whole corpus would accept a symbol that is
		// not in scope at the comment.
		byPkg := map[string][]parsed{}
		var order []string
		for _, p := range asts {
			if fc.Excluded(relTo(wd, p.name)) {
				continue
			}
			d := filepath.Dir(p.name)
			if _, ok := byPkg[d]; !ok {
				order = append(order, d)
			}
			byPkg[d] = append(byPkg[d], p)
		}
		sort.Strings(order)
		// Build the cross-package symbol index first: a reference like
		// [entitymanager.Manager] is resolved against the package it names,
		// not the one the comment lives in.
		corpus := Corpus{}
		for _, d := range order {
			files := make([]*ast.File, 0, len(byPkg[d]))
			for _, p := range byPkg[d] {
				files = append(files, p.file)
			}
			if len(files) > 0 {
				corpus[files[0].Name.Name] = NewSymbolTable(files)
			}
		}
		var found []DocLinkFinding
		for _, d := range order {
			ps := byPkg[d]
			files := make([]*ast.File, len(ps))
			names := make([]string, len(ps))
			for i, p := range ps {
				files[i], names[i] = p.file, p.name
			}
			for _, f := range FindDocLinks(fset, files, names, corpus) {
				if suppressed(fc, "doclink", relTo(wd, f.File), f.Doc) {
					continue
				}
				found = append(found, f)
			}
		}
		for i, f := range found {
			if *rank && i >= *top {
				break
			}
			fmt.Print(f.String(rel))
		}
		if len(found) > 0 {
			fmt.Printf("\n%d unresolvable doc links%s\n", len(found), showingOf(min(len(found), shownCount(*rank, *top, len(found))), len(found)))
			if !*rank {
				os.Exit(1)
			}
		} else {
			fmt.Printf("no unresolvable doc links across %d comments\n", total)
		}
		return
	}

	if cfg.Rules["param-contract"] {
		found := filterContract(FindParamContracts(corpus), fc, wd)
		for i, f := range found {
			if *rank && i >= *top {
				break
			}
			fmt.Print(f.String(rel))
		}
		if len(found) > 0 {
			fmt.Printf("\n%d asserted preconditions on unconstrained parameters%s\n", len(found), showingOf(shownCount(*rank, *top, len(found)), len(found)))
			if !*rank {
				os.Exit(1)
			}
		} else {
			fmt.Printf("no asserted preconditions across %d comments\n", total)
		}
		return
	}

	if cfg.Rules["nil-contract"] {
		found := filterNil(FindNilContracts(corpus), fc, wd)
		for i, f := range found {
			if *rank && i >= *top {
				break
			}
			fmt.Print(f.String(rel))
		}
		if len(found) > 0 {
			fmt.Printf("\n%d nil contracts stated as prose (standard form: `Nil: rejected|accepted|never returned — <why>`)%s\n", len(found), showingOf(shownCount(*rank, *top, len(found)), len(found)))
			if !*rank {
				os.Exit(1)
			}
		} else {
			fmt.Printf("no ad-hoc nil contracts across %d comments\n", total)
		}
		return
	}

	if cfg.Rules["duplication"] {
		clusters := Cluster(filterDup(FindDuplication(corpus, dupCfg), fc, wd), *sameName)
		shown := 0
		sites := 0
		for _, c := range clusters {
			fmt.Print(c.String(rel))
			shown++
			sites += len(c.Sites)
			if *rank && shown >= *top {
				break
			}
		}
		if shown > 0 {
			// Count the WHOLE result set, not the displayed slice. -top
			// bounds the output; it must never make the backlog look
			// smaller than it is.
			allSites := 0
			for _, c := range clusters {
				allSites += len(c.Sites)
			}
			fmt.Printf("\n%d duplicated facts across %d comment sites (corpus: %d comments)%s\n",
				len(clusters), allSites, total, showingOf(shown, len(clusters)))
			if !*rank {
				os.Exit(1)
			}
		} else {
			fmt.Printf("no duplicated paragraphs across %d comments\n", total)
		}
		return
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
		fmt.Printf("%s:%d: [%s/%s] %s\n", rel(f.File), f.Line, f.Level, f.Rule, f.Message)
		fmt.Printf("    │ %s\n", f.Excerpt)
		if len(f.Outside) > 0 {
			fmt.Printf("    └ out-of-scope terms: %s\n", strings.Join(f.Outside, ", "))
		}
	}

	if len(findings) > 0 {
		pct := 100 * float64(len(findings)) / float64(max(total, 1))
		fmt.Printf("\n%d findings across %d comments (%.1f%%)%s\n", len(findings), total, pct,
			showingOf(len(shown), len(findings)))
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

func relTo(wd, path string) string {
	if wd == "" {
		return path
	}
	if r, err := filepath.Rel(wd, path); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return path
}

func filterContract(in []ContractFinding, fc *FileConfig, wd string) []ContractFinding {
	out := in[:0]
	for _, f := range in {
		if suppressed(fc, "param-contract", relTo(wd, f.C.File), f.C.Text+"\n"+f.C.Trailer) {
			continue
		}
		out = append(out, f)
	}
	return out
}

func filterNil(in []NilFinding, fc *FileConfig, wd string) []NilFinding {
	out := in[:0]
	for _, f := range in {
		if suppressed(fc, "nil-contract", relTo(wd, f.C.File), f.C.Text+"\n"+f.C.Trailer) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// filterDup drops a pair when EITHER site is suppressed: a duplication finding
// names two places, and silencing one of them is a statement that the pairing
// is not a defect.
func filterDup(in []DupFinding, fc *FileConfig, wd string) []DupFinding {
	out := in[:0]
	for _, f := range in {
		if suppressed(fc, "duplication", relTo(wd, f.A.File), f.A.Text+"\n"+f.A.Trailer) ||
			suppressed(fc, "duplication", relTo(wd, f.B.File), f.B.Text+"\n"+f.B.Trailer) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// showingOf annotates a summary when -top hid part of the result set. A report
// that silently truncates reads as "this is everything", which is exactly the
// wrong impression for a backlog someone is tracking over time.
func showingOf(shown, total int) string {
	if shown >= total {
		return ""
	}
	return fmt.Sprintf(" — showing %d", shown)
}

// shownCount is how many findings -top left visible.
func shownCount(rank bool, top, total int) int {
	if rank && top < total {
		return top
	}
	return total
}
