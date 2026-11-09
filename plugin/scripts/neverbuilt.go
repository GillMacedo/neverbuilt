// Command neverbuilt finds Go files that none of your CI build configurations
// compile, because of build tags, GOOS or GOARCH file suffixes, or cgo, and
// checks whether those files still compile in a configuration that includes
// them.
//
// It uses only the standard library, so it runs with
//
//	go run neverbuilt.go [flags]
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/build"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

const version = "0.1.0"

// Exit codes.
const (
	exitClean    = 0
	exitFindings = 1
	exitUnusable = 2
)

// config is one way CI compiles the code: a port, build tags and cgo.
type config struct {
	GOOS   string   `json:"goos"`
	GOARCH string   `json:"goarch"`
	Tags   []string `json:"tags"`
	Cgo    bool     `json:"cgo"`
}

func (c config) String() string {
	s := c.GOOS + "/" + c.GOARCH
	parts := append([]string(nil), c.Tags...)
	if c.Cgo {
		parts = append(parts, "cgo")
	}
	if len(parts) > 0 {
		s += "," + strings.Join(parts, ",")
	}
	return s
}

func (c config) port() string { return c.GOOS + "/" + c.GOARCH }

// structuredConfig keeps tags as an empty JSON array when a config has none.
func structuredConfig(c config) config {
	c.Tags = append([]string{}, c.Tags...)
	return c
}

// env returns the environment for running the go command in this config.
// GOFLAGS -tags is overridden by the explicit -tags flag we pass.
func (c config) env() []string {
	cgo := "0"
	if c.Cgo {
		cgo = "1"
	}
	out := []string{}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "GOOS", "GOARCH", "CGO_ENABLED",
			// Architecture levels change which files build (amd64.v3 and
			// so on). CI's defaults are assumed, as MatchFile assumes them.
			"GOAMD64", "GOARM", "GOARM64", "GO386", "GOMIPS", "GOMIPS64", "GOPPC64", "GORISCV64", "GOWASM":
			continue
		}
		out = append(out, kv)
	}
	return append(out, "GOOS="+c.GOOS, "GOARCH="+c.GOARCH, "CGO_ENABLED="+cgo)
}

// usageError is bad usage or an input that can't be trusted. It exits 2.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func unusable(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

type portInfo struct {
	GOOS         string
	GOARCH       string
	CgoSupported bool
	FirstClass   bool
}

// goFile is one .go file found by walking the tree.
type goFile struct {
	Rel    string // slash-separated, relative to the scanned dir
	Abs    string
	Expr   constraint.Expr // nil if the file has no constraint line
	Line   string          // the constraint line as written
	Idents []string
	Pkg    string // package clause

	UsesCgo  bool // imports "C" or names cgo in its constraint
	ImportsC bool
}

// finding severities.
const (
	sevError = "error"
	sevWarn  = "warn"
	sevNote  = "note"
)

type finding struct {
	Severity string   `json:"severity"`
	Kind     string   `json:"kind"`
	File     string   `json:"file"`
	Config   string   `json:"config,omitempty"`
	Message  string   `json:"message"`
	Details  []string `json:"details,omitempty"`
}

type report struct {
	Tool             string    `json:"tool"`
	Version          string    `json:"version"`
	Dir              string    `json:"dir"`
	CI               []string  `json:"ci"`
	CIConfigs        []config  `json:"ci_configs"`
	Files            int       `json:"files"`
	Covered          int       `json:"covered"`
	Findings         []finding `json:"findings"`
	Suggested        []string  `json:"suggested,omitempty"`
	SuggestedConfigs []config  `json:"suggested_configs"`
	Errors           int       `json:"errors"`
	Warnings         int       `json:"warnings"`
	Compiled         bool      `json:"compiled"`
}

type options struct {
	dir        string
	ci         []config
	ignoreTags []string
	noCompile  bool
	format     string
	failOn     string
	goCmd      string
}

type ciFlag struct{ specs *[]string }

func (f ciFlag) String() string     { return "" }
func (f ciFlag) Set(s string) error { *f.specs = append(*f.specs, s); return nil }

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	rep, opts, err := runChecked(args, stdout)
	if err != nil {
		var ue *usageError
		if errors.As(err, &ue) {
			fmt.Fprintf(stderr, "neverbuilt: %s\n", ue.msg)
			fmt.Fprintln(stderr, "neverbuilt: no result; the input could not be checked")
			return exitUnusable
		}
		if errors.Is(err, flag.ErrHelp) {
			return exitClean
		}
		fmt.Fprintf(stderr, "neverbuilt: %v\n", err)
		return exitUnusable
	}
	if rep == nil { // --version
		return exitClean
	}
	if opts.format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintf(stderr, "neverbuilt: %v\n", err)
			return exitUnusable
		}
	} else {
		printText(stdout, rep)
	}
	switch opts.failOn {
	case "none":
		return exitClean
	case "error":
		if rep.Errors > 0 {
			return exitFindings
		}
	default:
		if rep.Errors+rep.Warnings > 0 {
			return exitFindings
		}
	}
	return exitClean
}

func runChecked(args []string, stdout io.Writer) (*report, options, error) {
	var opts options
	var specs []string
	var ignore string
	var showVersion bool
	fs := flag.NewFlagSet("neverbuilt", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.dir, "dir", ".", "directory to scan; `go list ./...` runs here")
	fs.Var(ciFlag{&specs}, "ci", "a configuration CI compiles, as GOOS/GOARCH[,tag...][,cgo]; repeat for each (default linux/amd64)")
	fs.StringVar(&ignore, "ignore-tag", "ignore,tools", "comma-separated tags that mark files never meant to compile")
	fs.BoolVar(&opts.noCompile, "no-compile", false, "only report files CI skips; don't compile them")
	fs.StringVar(&opts.format, "format", "text", "text or json")
	fs.StringVar(&opts.failOn, "fail-on", "warn", "lowest severity that exits 1: warn, error or none")
	fs.BoolVar(&showVersion, "version", false, "print the version")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usageText)
			return nil, opts, err
		}
		return nil, opts, unusable("%v\n%s", err, usageText)
	}
	if showVersion {
		fmt.Fprintln(stdout, "neverbuilt", version)
		return nil, opts, nil
	}
	if fs.NArg() > 0 {
		return nil, opts, unusable("unexpected argument %q; use --dir to choose the directory\n%s", fs.Arg(0), usageText)
	}
	switch opts.format {
	case "text", "json":
	default:
		return nil, opts, unusable("--format must be text or json, not %q", opts.format)
	}
	switch opts.failOn {
	case "warn", "error", "none":
	default:
		return nil, opts, unusable("--fail-on must be warn, error or none, not %q", opts.failOn)
	}
	for _, t := range strings.Split(ignore, ",") {
		if t = strings.TrimSpace(t); t != "" {
			opts.ignoreTags = append(opts.ignoreTags, t)
		}
	}
	opts.goCmd = "go"
	if g := os.Getenv("NEVERBUILT_GO"); g != "" {
		opts.goCmd = g
	}

	ports, err := listPorts(opts.goCmd, opts.dir)
	if err != nil {
		return nil, opts, err
	}
	if len(specs) == 0 {
		specs = []string{"linux/amd64"}
	}
	for _, s := range specs {
		c, err := parseSpec(s, ports)
		if err != nil {
			return nil, opts, err
		}
		opts.ci = append(opts.ci, c)
	}
	rep, err := check(opts, ports)
	return rep, opts, err
}

const usageText = `usage: neverbuilt [--dir DIR] [--ci GOOS/GOARCH[,tag...][,cgo]]... [--ignore-tag T,...]
                  [--no-compile] [--format text|json] [--fail-on warn|error|none]

Lists the Go files under DIR that none of the --ci configurations compile,
then compiles each one with go list -export in a configuration that includes it.
Exit 0: every file is compiled by a --ci configuration. Exit 1: findings.
Exit 2: bad usage or an input that couldn't be checked.
`

var tagRE = regexp.MustCompile(`^[A-Za-z0-9_.]+$`)

// parseSpec reads GOOS/GOARCH[,tag...]. The tag "cgo" sets CGO_ENABLED=1.
func parseSpec(s string, ports map[string]portInfo) (config, error) {
	var c config
	s = strings.TrimSpace(s)
	if s == "" {
		return c, unusable("--ci is empty; want GOOS/GOARCH[,tag...], such as linux/amd64,integration")
	}
	parts := strings.Split(s, ",")
	goos, goarch, ok := strings.Cut(strings.TrimSpace(parts[0]), "/")
	if !ok || goos == "" || goarch == "" {
		return c, unusable("--ci %q: want GOOS/GOARCH[,tag...], such as linux/amd64,integration", s)
	}
	p, known := ports[goos+"/"+goarch]
	if !known {
		return c, unusable("--ci %q: %s/%s is not a port this Go toolchain supports (see go tool dist list)", s, goos, goarch)
	}
	c.GOOS, c.GOARCH = goos, goarch
	seen := map[string]bool{}
	for _, t := range parts[1:] {
		t = strings.TrimSpace(t)
		if t == "" {
			return c, unusable("--ci %q: empty tag", s)
		}
		if !tagRE.MatchString(t) {
			return c, unusable("--ci %q: %q is not a valid build tag", s, t)
		}
		if t != "cgo" && (builtinTag(t, ports) || archLevelRE.MatchString(t)) {
			return c, unusable("--ci %q: %q is set by the platform or toolchain, not by -tags; write the platform as GOOS/GOARCH before the first comma", s, t)
		}
		if t == "cgo" {
			if !p.CgoSupported {
				return c, unusable("--ci %q: cgo isn't supported on %s/%s", s, goos, goarch)
			}
			c.Cgo = true
			continue
		}
		if !seen[t] {
			seen[t] = true
			c.Tags = append(c.Tags, t)
		}
	}
	sort.Strings(c.Tags)
	return c, nil
}

// builtinTag reports names that -tags must not set: GOOS, GOARCH, unix,
// compilers, release tags and GOEXPERIMENT tags.
func builtinTag(t string, ports map[string]portInfo) bool {
	return knownNames(ports)[t] || releaseRE.MatchString(t) || strings.HasPrefix(t, "goexperiment.")
}

// listPorts asks the go command which ports exist. It runs in dir, so the
// toolchain is the one dir's go.mod selects.
func listPorts(goCmd, dir string) (map[string]portInfo, error) {
	cmd := exec.Command(goCmd, "tool", "dist", "list", "-json")
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		cmd.Dir = dir
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, unusable("go tool dist list failed: %v %s", err, strings.TrimSpace(stderr.String()))
	}
	var list []portInfo
	if err := json.Unmarshal(out, &list); err != nil || len(list) == 0 {
		return nil, unusable("go tool dist list printed something that isn't a port list")
	}
	ports := map[string]portInfo{}
	for _, p := range list {
		ports[p.GOOS+"/"+p.GOARCH] = p
	}
	return ports, nil
}

func check(opts options, ports map[string]portInfo) (*report, error) {
	dir, err := filepath.Abs(opts.dir)
	if err != nil {
		return nil, unusable("%v", err)
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	st, err := os.Stat(dir)
	if err != nil {
		return nil, unusable("%v", err)
	}
	if !st.IsDir() {
		return nil, unusable("%s is not a directory", opts.dir)
	}
	if err := checkModule(opts.goCmd, dir); err != nil {
		return nil, err
	}
	files, ignoredBad, err := walk(dir, opts.ignoreTags)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, unusable("no Go files to check under %s, so there is nothing to check", opts.dir)
	}

	covered := map[string]bool{}
	for _, c := range opts.ci {
		got, err := listCovered(opts.goCmd, dir, c)
		if err != nil {
			return nil, err
		}
		for f := range got {
			covered[f] = true
		}
	}

	rep := &report{Tool: "neverbuilt", Version: version, Dir: opts.dir, Files: len(files) + len(ignoredBad), Compiled: !opts.noCompile}
	for _, c := range opts.ci {
		rep.CI = append(rep.CI, c.String())
		rep.CIConfigs = append(rep.CIConfigs, structuredConfig(c))
	}
	for _, rel := range ignoredBad {
		rep.Findings = append(rep.Findings, finding{
			Severity: sevNote, Kind: "ignored", File: rel,
			Message: "skipped: isn't valid Go, and its //go:build line needs an ignore tag, so go never builds it",
		})
	}
	known := knownNames(ports)
	order := portOrder(opts.ci, ports)

	type pending struct {
		f       *goFile
		cands   []config
		idx     int
		first   *compileResult // first failed attempt
		fcfg    string
		skipped []string // candidates that couldn't be compiled here
		skipWhy []string // one clause per skipped candidate
	}
	var todo []*pending
	// A misspelled tag is rare: one that many files use, or that CI sets,
	// is a real tag even if it looks like a GOOS or GOARCH.
	uses := map[string]int{}
	cgoDirs := map[string]bool{}
	for _, f := range files {
		if f.UsesCgo {
			cgoDirs[filepath.Dir(f.Abs)] = true
		}
	}
	for _, f := range files {
		for _, id := range f.Idents {
			uses[id]++
		}
	}
	for _, c := range opts.ci {
		for _, t := range c.Tags {
			uses[t] += 100
		}
	}
	for _, f := range files {
		for _, id := range f.Idents {
			if hint := typoHint(id, known, uses[id]); hint != "" {
				rep.Findings = append(rep.Findings, finding{
					Severity: sevWarn, Kind: "typo", File: f.Rel,
					Message: fmt.Sprintf("build tag %q looks like a misspelling of %q", id, hint),
					Details: []string{f.Line},
				})
			}
		}
		if covered[f.Abs] {
			rep.Covered++
			continue
		}
		if t := ignoredBy(f, opts.ignoreTags, known, order, ports); t != "" {
			rep.Findings = append(rep.Findings, finding{
				Severity: sevNote, Kind: "ignored", File: f.Rel,
				Message: fmt.Sprintf("skipped: needs tag %s, which marks files that aren't meant to compile", t),
			})
			continue
		}
		cands := findConfigs(f, known, order, maxTries, cgoDirs[filepath.Dir(f.Abs)], ports, opts.ignoreTags)
		if len(cands) == 0 {
			if why := specialNeed(f); why != "" {
				rep.Findings = append(rep.Findings, finding{
					Severity: sevWarn, Kind: "not-checked", File: f.Rel,
					Message: "never compiled by CI, and not checked: " + why,
					Details: lineDetails(f),
				})
				continue
			}
			if n := len(customTags(f, known, opts.ignoreTags)); n > 10 {
				rep.Findings = append(rep.Findings, finding{
					Severity: sevWarn, Kind: "not-checked", File: f.Rel,
					Message: fmt.Sprintf("never compiled by CI, and not checked: its constraint names %d tags, too many to search; add a --ci config that includes it", n),
					Details: lineDetails(f),
				})
				continue
			}
			rep.Findings = append(rep.Findings, finding{
				Severity: sevWarn, Kind: "unbuildable", File: f.Rel,
				Message: "no supported GOOS, GOARCH, tag set or cgo setting compiles this file",
				Details: lineDetails(f),
			})
			continue
		}
		todo = append(todo, &pending{f: f, cands: cands})
	}

	rep.SuggestedConfigs = []config{}
	suggested := map[string]bool{}
	suggest := func(c config) {
		k := c.String()
		if !suggested[k] {
			suggested[k] = true
			rep.Suggested = append(rep.Suggested, k)
			rep.SuggestedConfigs = append(rep.SuggestedConfigs, structuredConfig(c))
		}
	}
	host := runtime.GOOS + "/" + runtime.GOARCH
	byDir := map[string][]*goFile{}
	for _, f := range files {
		byDir[filepath.Dir(f.Abs)] = append(byDir[filepath.Dir(f.Abs)], f)
	}

	// Each round groups the pending files by their current candidate and
	// compiles each group once. A file that fails moves on to its next
	// candidate, so a file isn't called broken while some configuration
	// that includes it compiles.
	for len(todo) > 0 {
		groups := map[string][]*pending{}
		var keys []string
		for _, p := range todo {
			k := p.cands[p.idx].String()
			if _, ok := groups[k]; !ok {
				keys = append(keys, k)
			}
			groups[k] = append(groups[k], p)
		}
		sort.Strings(keys)
		var next []*pending
		for _, k := range keys {
			g := groups[k]
			cfg := g[0].cands[g[0].idx]
			if opts.noCompile {
				suggest(cfg)
				for _, p := range g {
					rep.Findings = append(rep.Findings, finding{
						Severity: sevWarn, Kind: "never-compiled", File: p.f.Rel, Config: k,
						Message: fmt.Sprintf("never compiled by CI; %s includes it (not compiled, --no-compile)", k),
					})
				}
				continue
			}
			var results map[string]compileResult
			if cfg.Cgo && cfg.port() != host {
				results = map[string]compileResult{}
				for _, p := range g {
					results[p.f.Abs] = compileResult{included: true, skipped: true,
						skipWhy: k + " wasn't compiled, because cgo for another platform needs a C cross-compiler"}
				}
			} else {
				var fs []*goFile
				results = map[string]compileResult{}
				for _, p := range g {
					if !mixedMain(p.f, cfg, byDir) {
						fs = append(fs, p.f)
						continue
					}
					one, err := compile(opts.goCmd, dir, cfg, []*goFile{p.f}, true)
					if err != nil {
						return nil, err
					}
					results[p.f.Abs] = one[p.f.Abs]
				}
				if len(fs) > 0 {
					more, err := compile(opts.goCmd, dir, cfg, fs, false)
					if err != nil {
						return nil, err
					}
					for k, v := range more {
						results[k] = v
					}
				}
			}
			for _, p := range g {
				r := results[p.f.Abs]
				if r.ok() {
					fd := finding{Severity: sevWarn, Kind: "never-compiled", File: p.f.Rel, Config: k,
						Message: fmt.Sprintf("never compiled by CI; compiles for %s", k)}
					if mixedMain(p.f, cfg, byDir) {
						// Adding k to CI would break go build ./..., so don't suggest it.
						fd.Message = fmt.Sprintf("never compiled by CI; compiles on its own for %s, as go run %s would build it, but go build ./... for %s fails because the directory also has package %s files",
							k, filepath.Base(p.f.Rel), k, otherPkg(p.f, byDir))
					} else {
						suggest(cfg)
					}
					if p.first != nil {
						fd.Details = append([]string{"fails for " + p.fcfg + ":"}, indent(p.first.lines())...)
					}
					rep.Findings = append(rep.Findings, fd)
					continue
				}
				if r.skipped {
					p.skipped = append(p.skipped, k)
					p.skipWhy = append(p.skipWhy, r.skipWhy)
				} else if p.first == nil {
					p.first, p.fcfg = &r, k
				}
				if p.idx+1 < len(p.cands) {
					p.idx++
					next = append(next, p)
					continue
				}
				fd := failure(p.f, p.first, p.fcfg, p.skipped, p.skipWhy, p.cands)
				rep.Findings = append(rep.Findings, fd)
				for _, candidate := range p.cands {
					if candidate.String() == fd.Config {
						suggest(candidate)
						break
					}
				}
			}
		}
		todo = next
	}

	sort.SliceStable(rep.Findings, func(i, j int) bool {
		a, b := rep.Findings[i], rep.Findings[j]
		if ra, rb := sevRank(a.Severity), sevRank(b.Severity); ra != rb {
			return ra < rb
		}
		return a.File < b.File
	})
	for _, f := range rep.Findings {
		switch f.Severity {
		case sevError:
			rep.Errors++
		case sevWarn:
			rep.Warnings++
		}
	}
	if rep.Findings == nil {
		rep.Findings = []finding{}
	}
	return rep, nil
}

func sevRank(s string) int {
	switch s {
	case sevError:
		return 0
	case sevWarn:
		return 1
	}
	return 2
}

func lineDetails(f *goFile) []string {
	if f.Line == "" {
		return nil
	}
	return []string{f.Line}
}

// checkModule makes sure dir is inside a module and go.mod is readable.
func checkModule(goCmd, dir string) error {
	cmd := exec.Command(goCmd, "env", "GOMOD")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return unusable("go env GOMOD failed in %s: %s", dir, firstLines(stderr.String(), err))
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull || strings.Contains(gomod, "\n") {
		return unusable("%s is not inside a module; neverbuilt needs a module with a go.mod", dir)
	}
	// go list reads go.mod for every configuration, and fails if it can't.
	return nil
}

func firstLines(s string, err error) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return err.Error()
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 8 {
		lines = append(lines[:8], "...")
	}
	return strings.Join(lines, "\n")
}

// walk finds the .go files that `go list ./...` could match: it skips
// testdata, vendor, directories and files starting with . or _, and nested
// modules. Every file is parsed in full; any that doesn't parse makes the
// whole run untrusted.
//
// A file that doesn't parse but whose //go:build line can only be true with
// an ignore tag, such as a code template, is returned in ignored instead.
func walk(root string, ignore []string) (files []*goFile, ignored []string, err error) {
	var bad []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return unusable("reading %s: %v", path, err)
		}
		name := d.Name()
		if d.IsDir() {
			if path == root {
				return nil
			}
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "vendor" {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			return nil
		}
		if !d.Type().IsRegular() {
			// go compiles a symlink to a regular file like the file itself.
			st, err := os.Stat(path)
			if err != nil || !st.Mode().IsRegular() {
				return nil
			}
		}
		rel, _ := filepath.Rel(root, path)
		f, msg := readGoFile(path)
		if msg != "" {
			if src, err := os.ReadFile(path); err == nil && headerNeedsIgnore(src, ignore) {
				ignored = append(ignored, filepath.ToSlash(rel))
				return nil
			}
			bad = append(bad, filepath.ToSlash(rel)+": "+msg)
			return nil
		}
		f.Rel = filepath.ToSlash(rel)
		f.Abs = path
		files = append(files, f)
		return nil
	})
	if err != nil {
		var ue *usageError
		if errors.As(err, &ue) {
			return nil, nil, err
		}
		return nil, nil, unusable("%v", err)
	}
	if len(bad) > 0 {
		return nil, nil, unusable("%d Go file(s) can't be read as Go source, so which files CI compiles can't be trusted:\n  %s", len(bad), strings.Join(bad, "\n  "))
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Rel < files[j].Rel })
	sort.Strings(ignored)
	return files, ignored, nil
}

// mixedMain reports whether f is a package main file that cfg builds next
// to files of another package in the same directory, like a generator run
// with go run gen.go. Such a file is compiled on its own.
func mixedMain(f *goFile, cfg config, byDir map[string][]*goFile) bool {
	if f.Pkg != "main" || strings.HasSuffix(f.Rel, "_test.go") {
		return false
	}
	for _, g := range byDir[filepath.Dir(f.Abs)] {
		if g == f || g.Pkg == "main" || strings.HasSuffix(g.Rel, "_test.go") {
			continue
		}
		if matchesConfig(g, cfg) {
			return true
		}
	}
	return false
}

func otherPkg(f *goFile, byDir map[string][]*goFile) string {
	for _, g := range byDir[filepath.Dir(f.Abs)] {
		if g.Pkg != f.Pkg && !strings.HasSuffix(g.Rel, "_test.go") {
			return g.Pkg
		}
	}
	return "other"
}

// matchesConfig reports whether cfg's build includes f.
func matchesConfig(f *goFile, cfg config) bool {
	if f.ImportsC && !cfg.Cgo {
		return false
	}
	ctxt := build.Default
	ctxt.GOOS, ctxt.GOARCH = cfg.GOOS, cfg.GOARCH
	ctxt.CgoEnabled = cfg.Cgo
	ctxt.BuildTags = cfg.Tags
	ctxt.ToolTags = toolTags(cfg.GOARCH)
	ok, err := ctxt.MatchFile(filepath.Dir(f.Abs), filepath.Base(f.Abs))
	return err == nil && ok
}

// headerNeedsIgnore reads the //go:build line at the top of src without
// parsing the rest, and reports whether it can only be true when one of the
// ignore tags is set. Every other tag, GOOS and GOARCH included, is treated
// as free, so the answer errs toward "no".
func headerNeedsIgnore(src []byte, ignore []string) bool {
	var x constraint.Expr
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "//") {
			break
		}
		if constraint.IsGoBuild(line) {
			y, err := constraint.Parse(line)
			if err != nil {
				return false
			}
			x = y
			break
		}
	}
	if x == nil {
		return false
	}
	var idents []string
	collectIdents(x, map[string]bool{}, &idents)
	var free []string
	for _, id := range idents {
		if !contains(ignore, id) {
			free = append(free, id)
		}
	}
	if len(free) > 14 || len(free) == len(idents) {
		return false
	}
	for mask := 0; mask < 1<<len(free); mask++ {
		set := map[string]bool{}
		for i, id := range free {
			if mask&(1<<i) != 0 {
				set[id] = true
			}
		}
		if x.Eval(func(tag string) bool { return set[tag] }) {
			return false
		}
	}
	return true
}

// readGoFile parses a file in full and reads its build constraint.
func readGoFile(path string) (*goFile, string) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err.Error()
	}
	if len(bytes.TrimSpace(src)) == 0 {
		return nil, "file is empty"
	}
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, oneLine(err.Error())
	}
	f := &goFile{Pkg: af.Name.Name}
	for _, imp := range af.Imports {
		if imp.Path.Value == `"C"` {
			f.UsesCgo, f.ImportsC = true, true
		}
	}
	var plus []string
	for _, cg := range af.Comments {
		if cg.Pos() >= af.Package {
			break
		}
		for _, c := range cg.List {
			text := c.Text
			if constraint.IsGoBuild(text) {
				if f.Expr != nil {
					return nil, "more than one //go:build line"
				}
				x, err := constraint.Parse(text)
				if err != nil {
					return nil, fmt.Sprintf("bad //go:build line %q: %v", text, err)
				}
				f.Expr, f.Line = x, text
			} else if constraint.IsPlusBuild(text) {
				if _, err := constraint.Parse(text); err != nil {
					return nil, fmt.Sprintf("bad // +build line %q: %v", text, err)
				}
				plus = append(plus, text)
			}
		}
	}
	if f.Expr == nil && len(plus) > 0 {
		var x constraint.Expr
		for _, p := range plus {
			y, _ := constraint.Parse(p)
			if x == nil {
				x = y
			} else {
				x = &constraint.AndExpr{X: x, Y: y}
			}
		}
		f.Expr, f.Line = x, strings.Join(plus, " ")
	}
	if f.Expr != nil {
		seen := map[string]bool{}
		collectIdents(f.Expr, seen, &f.Idents)
		sort.Strings(f.Idents)
		for _, id := range f.Idents {
			if id == "cgo" {
				f.UsesCgo = true
			}
		}
	}
	return f, ""
}

func collectIdents(x constraint.Expr, seen map[string]bool, out *[]string) {
	switch x := x.(type) {
	case *constraint.TagExpr:
		if !seen[x.Tag] {
			seen[x.Tag] = true
			*out = append(*out, x.Tag)
		}
	case *constraint.NotExpr:
		collectIdents(x.X, seen, out)
	case *constraint.AndExpr:
		collectIdents(x.X, seen, out)
		collectIdents(x.Y, seen, out)
	case *constraint.OrExpr:
		collectIdents(x.X, seen, out)
		collectIdents(x.Y, seen, out)
	}
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " ..."
	}
	return s
}

// listCovered returns the absolute paths of every file that go list says
// this configuration compiles, with tests.
func listCovered(goCmd, dir string, c config) (map[string]bool, error) {
	args := []string{"list", "-e", "-tags=" + strings.Join(c.Tags, ","),
		"-json=Dir,ImportPath,GoFiles,CgoFiles,TestGoFiles,XTestGoFiles,InvalidGoFiles,Error", "./..."}
	pkgs, err := goList(goCmd, dir, c, args)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, p := range pkgs {
		// A file go can't build in this package, such as a package main
		// file among package p files, isn't compiled by this config.
		invalid := map[string]bool{}
		for _, name := range p.InvalidGoFiles {
			invalid[name] = true
		}
		for _, list := range [][]string{p.GoFiles, p.CgoFiles, p.TestGoFiles, p.XTestGoFiles} {
			for _, name := range list {
				if !invalid[name] {
					out[filepath.Join(p.Dir, name)] = true
				}
			}
		}
	}
	return out, nil
}

type listError struct {
	Pos string
	Err string
}

type listPkg struct {
	Dir            string
	ImportPath     string
	ForTest        string
	GoFiles        []string
	CgoFiles       []string
	TestGoFiles    []string
	XTestGoFiles   []string
	Error          *listError
	DepsErrors     []*listError
	Export         string
	InvalidGoFiles []string
}

func goList(goCmd, dir string, c config, args []string) ([]listPkg, error) {
	cmd := exec.Command(goCmd, args...)
	cmd.Dir = dir
	cmd.Env = c.env()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, unusable("go %s failed for %s: %s", strings.Join(args, " "), c, firstLines(stderr.String(), err))
	}
	var pkgs []listPkg
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p listPkg
		err := dec.Decode(&p)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, unusable("go list output for %s isn't valid JSON: %v", c, err)
		}
		if p.Dir == "" && p.ForTest == "" && !strings.HasSuffix(p.ImportPath, ".test") {
			why := ""
			if p.Error != nil {
				why = ": " + oneLine(p.Error.Err)
			}
			if p.Error != nil && strings.Contains(p.Error.Err, "malformed import path") {
				return nil, unusable("go list for %s found a directory whose name is not a valid import path (%q): %s", c, p.ImportPath, oneLine(p.Error.Err))
			}
			return nil, unusable("go list output for %s has a package with no directory (%q)%s", c, p.ImportPath, why)
		}
		if real, err := filepath.EvalSymlinks(p.Dir); err == nil {
			p.Dir = real
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, nil
}

type compileResult struct {
	included bool
	skipped  bool   // not compiled, or its C code failed: no verdict
	skipWhy  string // a clause saying why
	// testsOnly: the file and its package compile, but the package's
	// tests don't.
	testsOnly bool
	own       []string // compiler errors located in this file
	other     []string // errors elsewhere in the package, or with no position
}

func (r compileResult) ok() bool {
	return r.included && !r.skipped && len(r.own) == 0 && len(r.other) == 0
}

func (r compileResult) lines() []string {
	out := append(append([]string(nil), r.own...), r.other...)
	if len(out) > 6 {
		out = append(out[:6], "...")
	}
	return out
}

func indent(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = "  " + l
	}
	return out
}

// failure builds the finding for a file that no candidate compiled. The
// first candidate that really compiled decides the message. A compile error
// is only an error when no candidate was skipped, since a skipped cgo build
// might have compiled.
func failure(f *goFile, r *compileResult, k string, skipped, skipWhy []string, cands []config) finding {
	fd := finding{File: f.Rel, Config: k, Kind: "never-compiled", Severity: sevWarn}
	skipNote := ""
	if len(skipped) > 0 {
		skipNote = "; " + strings.Join(skipWhy, "; ")
	}
	if r == nil {
		fd.Config = skipped[0]
		fd.Message = "never compiled by CI, and not checked: " + strings.Join(skipWhy, "; ")
		return fd
	}
	switch {
	case len(r.own) > 0:
		if len(skipped) == 0 || definite(r.own) {
			fd.Severity = sevError
			fd.Kind = "compile-error"
		}
		fd.Message = fmt.Sprintf("never compiled by CI, and fails to compile for %s", k)
		fd.Details = r.own
	case !r.included:
		fd.Message = fmt.Sprintf("never compiled by CI; expected %s to include it, but go list left it out, so it wasn't compiled", k)
	case r.testsOnly:
		fd.Message = fmt.Sprintf("never compiled by CI; compiles for %s, but its package's tests don't", k)
		fd.Details = r.other
	default:
		fd.Message = fmt.Sprintf("never compiled by CI; couldn't confirm it compiles, because its package fails for %s", k)
		fd.Details = r.other
	}
	fd.Message += skipNote
	var others []string
	for _, c := range cands {
		if n := c.String(); n != k && !contains(skipped, n) {
			others = append(others, n)
		}
	}
	if len(others) > 0 {
		fd.Details = append(fd.Details, "also fails for: "+strings.Join(others, "  "))
	}
	return fd
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

var posRE = regexp.MustCompile(`^(.+?\.go):(\d+)(?::(\d+))?: (.*)$`)

// cErrorRE matches C compiler and cgo tool failures. gcc and clang put
// "error:" or "fatal error:" right after the position; Go errors never do.
var cErrorRE = regexp.MustCompile(`^[^ ]+:\d+(:\d+)?: (fatal )?error: |C compiler "[^"]*" not found|^cgo: |^compilation terminated|exec: "[^"]*(gcc|clang|cc)"`)

// crossFileRE matches compiler errors that another file in the package
// could explain, such as a cgo file that wasn't built.
var crossFileRE = regexp.MustCompile(`undefined: | undefined \(type |redeclared in this block|other declaration of|not declared by package|found packages |: too many errors$`)

// definite reports whether some error line can't be explained by a file
// that a skipped build would have added.
func definite(lines []string) bool {
	for _, l := range lines {
		if !crossFileRE.MatchString(l) {
			return true
		}
	}
	return false
}

// cContextRE matches the source excerpt lines gcc and clang print.
var cContextRE = regexp.MustCompile(`^\s*(\d+\s*)?\|`)

// compile runs go list -export -test for the packages holding files, in cfg,
// which really compiles each package and its test variants without linking.
// With standalone set, files holds one file, compiled on its own as
// go run would.
func compile(goCmd, dir string, cfg config, files []*goFile, standalone bool) (map[string]compileResult, error) {
	dirs := map[string]bool{}
	var pats []string
	for _, f := range files {
		if standalone {
			pats = append(pats, "./"+f.Rel)
			continue
		}
		d := filepath.Dir(f.Rel)
		if !dirs[d] {
			dirs[d] = true
			pats = append(pats, "./"+filepath.ToSlash(d))
		}
	}
	sort.Strings(pats)
	args := []string{"list", "-e", "-export"}
	if !standalone {
		args = append(args, "-test")
	}
	args = append(append(args, "-tags="+strings.Join(cfg.Tags, ","),
		"-json=Dir,ImportPath,ForTest,GoFiles,CgoFiles,TestGoFiles,XTestGoFiles,Error,DepsErrors,Export"), pats...)
	pkgs, err := goList(goCmd, dir, cfg, args)
	if err != nil {
		return nil, err
	}
	included := map[string]bool{}
	byFile := map[string][]string{}
	byDir := map[string][]string{}
	plainErr := map[string]bool{} // dirs whose non-test package fails
	seen := map[string]bool{}
	add := func(pkgDir string, isTest bool, e *listError) {
		if e == nil || e.Err == "" {
			return
		}
		for i, line := range strings.Split(e.Err, "\n") {
			line = strings.TrimRight(line, "\r")
			if line == "" || strings.HasPrefix(line, "# ") {
				continue
			}
			if i == 0 && e.Pos != "" && !posRE.MatchString(line) {
				line = e.Pos + ": " + line
			}
			line = strings.ReplaceAll(line, dir+string(filepath.Separator), "")
			line = strings.ReplaceAll(line, dir, ".")
			if cContextRE.MatchString(line) {
				continue
			}
			if seen[pkgDir+"\x00"+line] {
				continue
			}
			seen[pkgDir+"\x00"+line] = true
			if m := posRE.FindStringSubmatch(line); m != nil {
				p := m[1]
				if !filepath.IsAbs(p) {
					p = filepath.Join(dir, p)
				}
				// Resolve the directory only: a symlinked file keeps its name.
				if real, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
					p = filepath.Join(real, filepath.Base(p))
				}
				rel, err := filepath.Rel(dir, p)
				if err == nil && !strings.HasPrefix(rel, "..") {
					line = filepath.ToSlash(rel) + line[len(m[1]):]
				}
				if !seen[p+"\x00"+line] {
					seen[p+"\x00"+line] = true
					byFile[p] = append(byFile[p], line)
				}
			}
			byDir[pkgDir] = append(byDir[pkgDir], line)
			if !isTest {
				plainErr[pkgDir] = true
			}
		}
	}
	// Plain packages first, so an error shared with the test variants is
	// counted as the package's own.
	for _, pass := range []bool{false, true} {
		for _, p := range pkgs {
			isTest := p.ForTest != "" || strings.HasSuffix(p.ImportPath, ".test") || strings.Contains(p.ImportPath, " [")
			if isTest != pass {
				continue
			}
			for _, list := range [][]string{p.GoFiles, p.CgoFiles, p.TestGoFiles, p.XTestGoFiles} {
				for _, name := range list {
					included[filepath.Join(p.Dir, name)] = true
				}
			}
			add(p.Dir, isTest, p.Error)
			for _, e := range p.DepsErrors {
				add(p.Dir, isTest, e)
			}
			// No error and no export data means nothing was compiled, so
			// there is no evidence that it compiles.
			if p.Export == "" && p.Error == nil && len(p.DepsErrors) == 0 && len(p.GoFiles)+len(p.CgoFiles) > 0 {
				add(p.Dir, isTest, &listError{Err: "go list -export returned no compiled output for " + p.ImportPath})
			}
		}
	}
	res := map[string]compileResult{}
	for _, f := range files {
		r := compileResult{included: included[f.Abs], own: byFile[f.Abs]}
		own := map[string]bool{}
		for _, line := range r.own {
			own[line] = true
		}
		for _, line := range byDir[filepath.Dir(f.Abs)] {
			if !own[line] {
				r.other = append(r.other, line)
			}
		}
		if len(r.other) > 6 {
			r.other = append(r.other[:6], "...")
		}
		r.testsOnly = len(r.own) == 0 && len(r.other) > 0 && !plainErr[filepath.Dir(f.Abs)] && !strings.HasSuffix(f.Abs, "_test.go")
		if cfg.Cgo {
			// A C compiler error usually means a missing header, library or
			// toolchain on this machine, so it's no verdict on the Go code.
			for _, line := range byDir[filepath.Dir(f.Abs)] {
				if cErrorRE.MatchString(line) {
					r = compileResult{included: r.included, skipped: true,
						skipWhy: fmt.Sprintf("%s wasn't checked, because its C code doesn't compile on this machine (%s)", cfg, line)}
					break
				}
			}
		}
		res[f.Abs] = r
	}
	return res, nil
}

// Names go/build treats as GOOS and GOARCH, including old ports, so that a
// file suffix or tag naming them isn't taken for a custom tag.
var (
	knownOS   = strings.Fields("aix android darwin dragonfly freebsd hurd illumos ios js linux nacl netbsd openbsd plan9 solaris wasip1 windows zos")
	knownArch = strings.Fields("386 amd64 amd64p32 arm armbe arm64 arm64be loong64 mips mipsle mips64 mips64le mips64p32 mips64p32le ppc ppc64 ppc64le riscv riscv64 s390 s390x sparc sparc64 wasm")
)

func knownNames(ports map[string]portInfo) map[string]bool {
	m := map[string]bool{"unix": true, "cgo": true, "gc": true, "gccgo": true}
	for _, s := range knownOS {
		m[s] = true
	}
	for _, s := range knownArch {
		m[s] = true
	}
	for _, p := range ports {
		m[p.GOOS] = true
		m[p.GOARCH] = true
	}
	return m
}

var releaseRE = regexp.MustCompile(`^go\d+\.\d+$`)

// customTags are the identifiers in f's constraint that only -tags can set.
// Tags in exclude, such as the ignore tags, are never set.
func customTags(f *goFile, known map[string]bool, exclude []string) []string {
	var out []string
	for _, id := range f.Idents {
		if known[id] || releaseRE.MatchString(id) || strings.HasPrefix(id, "goexperiment.") || archLevelRE.MatchString(id) || contains(exclude, id) {
			continue
		}
		out = append(out, id)
	}
	return out
}

// archLevelRE matches architecture feature tags such as amd64.v3, arm.7,
// arm64.v8.1 and 386.sse2, which come from GOAMD64, GOARM and friends.
var archLevelRE = regexp.MustCompile(`^(386|amd64|arm|arm64|loong64|mips|mipsle|mips64|mips64le|ppc64|ppc64le|riscv64|s390x|wasm)\.`)

// preferred is the order for suggesting a port.
var preferred = []string{"linux/amd64", "linux/arm64", "darwin/arm64", "darwin/amd64", "windows/amd64", "linux/386", "linux/arm", "windows/386", "windows/arm64"}

func portOrder(ci []config, ports map[string]portInfo) []portInfo {
	var out []portInfo
	seen := map[string]bool{}
	add := func(k string) {
		if p, ok := ports[k]; ok && !seen[k] {
			seen[k] = true
			out = append(out, p)
		}
	}
	for _, c := range ci {
		add(c.port())
	}
	for _, k := range preferred {
		add(k)
	}
	var first, rest []string
	for k, p := range ports {
		if p.FirstClass {
			first = append(first, k)
		} else {
			rest = append(rest, k)
		}
	}
	byArch := func(list []string) {
		sort.Slice(list, func(i, j int) bool {
			ai, aj := archRank(list[i]), archRank(list[j])
			if ai != aj {
				return ai < aj
			}
			return list[i] < list[j]
		})
	}
	byArch(first)
	byArch(rest)
	var mobile []string
	for _, k := range append(first, rest...) {
		// android and ios need cgo to link, so they come last.
		if strings.HasPrefix(k, "android/") || strings.HasPrefix(k, "ios/") {
			mobile = append(mobile, k)
			continue
		}
		add(k)
	}
	for _, k := range mobile {
		add(k)
	}
	return out
}

// archRank prefers the common 64-bit architectures when a file only needs
// some GOOS.
func archRank(port string) int {
	_, arch, _ := strings.Cut(port, "/")
	switch arch {
	case "amd64":
		return 0
	case "arm64":
		return 1
	}
	return 2
}

// maxTries is how many configurations are compiled for one file before it
// is reported as broken.
const maxTries = 4

// findConfigs lists up to max configurations that include f, simplest
// first: the fewest tags, then ports in portOrder, then cgo off before on.
// Each tag set contributes its first matching port.
// When the file's directory uses cgo, a cgo-off match is followed by the
// same configuration with cgo on, because the package may need its cgo files.
func findConfigs(f *goFile, known map[string]bool, order []portInfo, max int, dirCgo bool, ports map[string]portInfo, exclude []string) []config {
	tags := customTags(f, known, exclude)
	var out []config
	for _, subset := range subsets(tags, f.Expr) {
		c, ok := matchPort(f, subset, order, false)
		if !ok {
			continue
		}
		if c.Cgo {
			// Prefer the host for cgo, the one port where a C compiler is likely.
			if h, ok := matchPort(f, subset, hostFirst(ports), true); ok {
				c = h
			}
		}
		out = append(out, c)
		if dirCgo && !c.Cgo && ports[c.port()].CgoSupported {
			if h, ok := matchPort(f, subset, append(hostFirst(ports), ports[c.port()]), true); ok {
				out = append(out, h)
			}
		}
		if len(out) >= max {
			return out[:max]
		}
	}
	return out
}

// matchPort returns the first port in order whose build includes f. With
// cgoOnly, only cgo builds are tried.
func matchPort(f *goFile, tags []string, order []portInfo, cgoOnly bool) (config, bool) {
	for _, p := range order {
		for _, cgo := range []bool{false, true} {
			if cgo && !p.CgoSupported {
				continue
			}
			// go/build's MatchFile doesn't drop files that import "C" when
			// cgo is off; Import does. Do the same here.
			if (cgoOnly || f.ImportsC) && !cgo {
				continue
			}
			ctxt := build.Default
			ctxt.GOOS, ctxt.GOARCH = p.GOOS, p.GOARCH
			ctxt.CgoEnabled = cgo
			ctxt.BuildTags = tags
			ctxt.ToolTags = toolTags(p.GOARCH)
			ok, err := ctxt.MatchFile(filepath.Dir(f.Abs), filepath.Base(f.Abs))
			if err == nil && ok {
				return config{GOOS: p.GOOS, GOARCH: p.GOARCH, Tags: tags, Cgo: cgo}, true
			}
		}
	}
	return config{}, false
}

// toolTags keeps goexperiment tags from the default context and sets the
// default architecture level tags of the target.
// hostFirst returns just the host port, if this toolchain knows it.
func hostFirst(ports map[string]portInfo) []portInfo {
	if p, ok := ports[runtime.GOOS+"/"+runtime.GOARCH]; ok {
		return []portInfo{p}
	}
	return nil
}

func toolTags(goarch string) []string {
	var out []string
	for _, t := range build.Default.ToolTags {
		if strings.HasPrefix(t, "goexperiment.") {
			out = append(out, t)
		}
	}
	switch goarch {
	case "amd64":
		out = append(out, "amd64.v1")
	case "arm64":
		out = append(out, "arm64.v8.0")
	}
	return out
}

// subsets lists tag subsets, smallest first. Past 10 tags it tries none,
// each one alone, the tags the constraint names without a !, and all.
func subsets(tags []string, x constraint.Expr) [][]string {
	n := len(tags)
	if n > 10 {
		out := [][]string{nil}
		for _, t := range tags {
			out = append(out, []string{t})
		}
		pos := map[string]bool{}
		positives(x, false, pos)
		var ps []string
		for _, t := range tags {
			if pos[t] {
				ps = append(ps, t)
			}
		}
		return append(out, ps, append([]string(nil), tags...))
	}
	var out [][]string
	for size := 0; size <= n; size++ {
		for mask := 0; mask < 1<<n; mask++ {
			if bitCount(mask) != size {
				continue
			}
			var s []string
			for i := 0; i < n; i++ {
				if mask&(1<<i) != 0 {
					s = append(s, tags[i])
				}
			}
			out = append(out, s)
		}
	}
	return out
}

// positives collects tags that appear under an even number of negations.
func positives(x constraint.Expr, neg bool, out map[string]bool) {
	switch x := x.(type) {
	case *constraint.TagExpr:
		if !neg {
			out[x.Tag] = true
		}
	case *constraint.NotExpr:
		positives(x.X, !neg, out)
	case *constraint.AndExpr:
		positives(x.X, neg, out)
		positives(x.Y, neg, out)
	case *constraint.OrExpr:
		positives(x.X, neg, out)
		positives(x.Y, neg, out)
	}
}

func bitCount(x int) int {
	n := 0
	for ; x != 0; x &= x - 1 {
		n++
	}
	return n
}

// ignoredBy returns the ignore tag a file needs: one it names, when no
// configuration without the ignore tags includes the file but one with them
// does. A file that says !ignore isn't ignored.
func ignoredBy(f *goFile, ignore []string, known map[string]bool, order []portInfo, ports map[string]portInfo) string {
	named := ""
	for _, id := range f.Idents {
		if contains(ignore, id) {
			named = id
			break
		}
	}
	if named == "" {
		return ""
	}
	if len(findConfigs(f, known, order, 1, false, ports, ignore)) > 0 {
		return ""
	}
	if len(findConfigs(f, known, order, 1, false, ports, nil)) == 0 {
		return ""
	}
	return named
}

// specialNeed explains files that need something neverbuilt doesn't try:
// gccgo, a newer Go release, a GOEXPERIMENT or an architecture level.
func specialNeed(f *goFile) string {
	var needs []string
	for _, id := range f.Idents {
		switch {
		case id == "gccgo":
			needs = append(needs, "the gccgo compiler")
		case releaseRE.MatchString(id):
			if !hasRelease(id) {
				needs = append(needs, "Go release "+id+" or newer, which is newer than this toolchain")
			}
		case strings.HasPrefix(id, "goexperiment."):
			needs = append(needs, "GOEXPERIMENT="+strings.TrimPrefix(id, "goexperiment."))
		case archLevelRE.MatchString(id):
			needs = append(needs, "architecture level "+id)
		}
	}
	if len(needs) == 0 {
		return ""
	}
	return "may need " + strings.Join(needs, ", or ")
}

func hasRelease(tag string) bool {
	for _, t := range build.Default.ReleaseTags {
		if t == tag {
			return true
		}
	}
	return false
}

// typoHint returns the GOOS, GOARCH or special name that id seems to be a
// misspelling of, or "".
func typoHint(id string, known map[string]bool, uses int) string {
	if known[id] || otherToolchains[id] || len(id) < 4 || strings.Contains(id, ".") {
		return ""
	}
	lower := strings.ToLower(id)
	if lower != id && known[lower] {
		return lower // Linux, Windows
	}
	if len(id) < 5 || uses > 2 {
		return ""
	}
	var names []string
	for k := range known {
		names = append(names, k)
	}
	sort.Strings(names)
	best, bestD := "", 99
	for _, k := range names {
		if len(k) < 4 {
			continue
		}
		if lower == k {
			return k
		}
		if letters(lower) == letters(k) {
			continue // wasip2 and wasip1 differ only in digits: a different name, not a typo
		}
		d := levenshtein(lower, k)
		limit := 1
		if len(k) >= 6 {
			limit = 2
		}
		if d <= limit && d < bestD {
			best, bestD = k, d
		}
	}
	return best
}

// otherToolchains are tags that TinyGo, GopherJS, App Engine and other
// builds set, which look close to a GOOS or GOARCH but are real names.
var otherToolchains = map[string]bool{
	"wasi": true, "tinygo": true, "gopherjs": true, "appengine": true,
	"haiku": true, "purego": true, "baremetal": true,
}

func letters(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return -1
		}
		return r
	}, s)
}

func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func printText(w io.Writer, r *report) {
	bw := bufio.NewWriter(w)
	defer bw.Flush()
	fmt.Fprintf(bw, "neverbuilt %s: %d Go files; CI configs: %s\n", r.Dir, r.Files, strings.Join(r.CI, "  "))
	for _, f := range r.Findings {
		label := map[string]string{sevError: "ERROR", sevWarn: "WARN ", sevNote: "note "}[f.Severity]
		fmt.Fprintf(bw, "  %s %s\n", label, f.File)
		fmt.Fprintf(bw, "        %s\n", f.Message)
		for _, d := range f.Details {
			fmt.Fprintf(bw, "        %s\n", d)
		}
	}
	if len(r.Suggested) > 0 {
		fmt.Fprintf(bw, "  To compile every file, also run CI with: %s\n", strings.Join(r.Suggested, "  "))
	}
	if r.Errors+r.Warnings == 0 {
		if r.Covered == r.Files {
			fmt.Fprintf(bw, "  All %d Go files are compiled by at least one CI config.\n", r.Files)
		} else {
			rest := r.Files - r.Covered
			verb := "are"
			if rest == 1 {
				verb = "is"
			}
			fmt.Fprintf(bw, "  %d of %d Go files are compiled by a CI config; the other %d %s skipped on purpose (notes above).\n", r.Covered, r.Files, rest, verb)
		}
		return
	}
	fmt.Fprintf(bw, "  %s, %s. %d of %d Go files are compiled by a CI config.\n",
		plural(r.Errors, "error"), plural(r.Warnings, "warning"), r.Covered, r.Files)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
