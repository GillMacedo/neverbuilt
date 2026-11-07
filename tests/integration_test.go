// Package tests runs the neverbuilt command against real go list output and
// real compiles by the go toolchain on the test machine.
package tests

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

var (
	buildOnce sync.Once
	toolPath  string
	buildErr  error
	buildOut  []byte
)

// tool builds the neverbuilt command once per test run.
func tool(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "neverbuilt-tool")
		if err != nil {
			buildErr = err
			return
		}
		exe := ""
		if runtime.GOOS == "windows" {
			exe = ".exe"
		}
		toolPath = filepath.Join(dir, "neverbuilt"+exe)
		cmd := exec.Command("go", "build", "-o", toolPath, "./plugin/scripts")
		cmd.Dir = ".."
		buildOut, buildErr = cmd.CombinedOutput()
	})
	if buildErr != nil {
		t.Fatalf("building neverbuilt: %v\n%s", buildErr, buildOut)
	}
	return toolPath
}

type result struct {
	stdout, stderr string
	code           int
}

func runTool(t *testing.T, env []string, args ...string) result {
	t.Helper()
	cmd := exec.Command(tool(t), args...)
	cmd.Env = append(os.Environ(), env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return result{out.String(), errb.String(), code}
}

func examples(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "examples", name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// copyDir copies a fixture module so a test can damage it.
func copyDir(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func writeModule(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if _, ok := files["go.mod"]; !ok {
		files["go.mod"] = "module example.com/m\n\ngo 1.22\n"
	}
	for name, src := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// fixedCI is the CI matrix that compiles every file of shipyard-fixed.
var fixedCI = []string{"--ci", "linux/amd64", "--ci", "linux/amd64,integration", "--ci", "windows/amd64"}

func args(dir string, extra ...string) []string {
	return append([]string{"--dir", dir}, extra...)
}

func mustContain(t *testing.T, r result, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(r.stdout, s) {
			t.Errorf("output lacks %q:\n%s%s", s, r.stdout, r.stderr)
		}
	}
}

// assertClean checks a quiet, passing result.
func assertClean(t *testing.T, r result) {
	t.Helper()
	if r.code != 0 {
		t.Fatalf("exit %d, want 0:\n%s%s", r.code, r.stdout, r.stderr)
	}
	if strings.Contains(r.stdout, "ERROR") || strings.Contains(r.stdout, "WARN") {
		t.Errorf("healthy input has findings:\n%s", r.stdout)
	}
}

// assertUntrusted checks that a run that couldn't read its input exits 2 and
// prints nothing that looks like a result.
func assertUntrusted(t *testing.T, r result, why string) {
	t.Helper()
	if r.code != 2 {
		t.Errorf("%s: exit %d, want 2\nstdout:\n%s\nstderr:\n%s", why, r.code, r.stdout, r.stderr)
	}
	if r.stdout != "" {
		t.Errorf("%s: printed a result to stdout:\n%s", why, r.stdout)
	}
	if !strings.Contains(r.stderr, "no result") {
		t.Errorf("%s: stderr doesn't say there is no result:\n%s", why, r.stderr)
	}
}

func TestShipyardFindsEveryMistake(t *testing.T) {
	r := runTool(t, nil, args(examples(t, "shipyard"))...)
	if r.code != 1 {
		t.Fatalf("exit %d, want 1:\n%s%s", r.code, r.stdout, r.stderr)
	}
	mustContain(t, r,
		"ERROR clock/label_windows.go",
		"label redeclared in this block",
		"ERROR store/lock_windows.go",
		"undefined: writeFile",
		"ERROR store/store_integration_test.go",
		"unknown field Size in struct literal",
		`build tag "widows" looks like a misspelling of "windows"`,
		"WARN  legacy/legacy_plan9.go",
		"no supported GOOS, GOARCH, tag set or cgo setting compiles this file",
		"note  tools/gen.go",
		"also run CI with: linux/amd64,integration  windows/amd64",
		"3 errors, 2 warnings. 7 of 12 Go files",
	)
}

func TestShipyardJSON(t *testing.T) {
	r := runTool(t, nil, args(examples(t, "shipyard"), "--format", "json")...)
	if r.code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", r.code, r.stderr)
	}
	var rep struct {
		Files, Covered, Errors, Warnings int
		Findings                         []struct{ Severity, Kind, File, Config string }
		Suggested                        []string
	}
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, r.stdout)
	}
	if rep.Files != 12 || rep.Covered != 7 || rep.Errors != 3 || rep.Warnings != 2 {
		t.Errorf("counts = %+v", rep)
	}
	kinds := map[string]string{}
	for _, f := range rep.Findings {
		kinds[f.File] += f.Kind + " "
	}
	want := map[string]string{
		"clock/label_windows.go":          "compile-error ",
		"store/lock_windows.go":           "compile-error ",
		"store/store_integration_test.go": "compile-error ",
		"clock/label_other.go":            "typo ",
		"legacy/legacy_plan9.go":          "unbuildable ",
		"tools/gen.go":                    "ignored ",
	}
	for f, k := range want {
		if kinds[f] != k {
			t.Errorf("%s: kinds %q, want %q", f, kinds[f], k)
		}
	}
}

func TestFixedIsQuietWithFullMatrix(t *testing.T) {
	r := runTool(t, nil, args(examples(t, "shipyard-fixed"), fixedCI...)...)
	assertClean(t, r)
	mustContain(t, r, "10 of 11 Go files are compiled by a CI config; the other 1 is skipped on purpose")
}

func TestFixedWithLinuxOnly(t *testing.T) {
	r := runTool(t, nil, args(examples(t, "shipyard-fixed"))...)
	if r.code != 1 {
		t.Fatalf("exit %d, want 1:\n%s%s", r.code, r.stdout, r.stderr)
	}
	mustContain(t, r,
		"WARN  clock/label_windows.go",
		"never compiled by CI; compiles for windows/amd64",
		"WARN  store/store_integration_test.go",
		"never compiled by CI; compiles for linux/amd64,integration",
		"0 errors, 3 warnings.",
	)
	if strings.Contains(r.stdout, "ERROR") {
		t.Errorf("fixed code reported an error:\n%s", r.stdout)
	}
	// Warnings only: --fail-on error passes, and says so honestly.
	r = runTool(t, nil, args(examples(t, "shipyard-fixed"), "--fail-on", "error")...)
	if r.code != 0 || !strings.Contains(r.stdout, "0 errors, 3 warnings.") {
		t.Errorf("--fail-on error: exit %d\n%s", r.code, r.stdout)
	}
}

func TestFailOn(t *testing.T) {
	dir := examples(t, "shipyard")
	for _, c := range []struct {
		failOn string
		code   int
	}{{"warn", 1}, {"error", 1}, {"none", 0}} {
		r := runTool(t, nil, args(dir, "--fail-on", c.failOn)...)
		if r.code != c.code {
			t.Errorf("--fail-on %s: exit %d, want %d", c.failOn, r.code, c.code)
		}
		// The report never turns clean, whatever the exit code.
		if !strings.Contains(r.stdout, "3 errors, 2 warnings.") || strings.Contains(r.stdout, "All ") {
			t.Errorf("--fail-on %s changed the report:\n%s", c.failOn, r.stdout)
		}
	}
}

func TestNoCompile(t *testing.T) {
	r := runTool(t, nil, args(examples(t, "shipyard"), "--no-compile")...)
	if r.code != 1 {
		t.Fatalf("exit %d, want 1", r.code)
	}
	mustContain(t, r, "(not compiled, --no-compile)")
	if strings.Contains(r.stdout, "ERROR") {
		t.Errorf("--no-compile can't know about compile errors:\n%s", r.stdout)
	}
}

// Every way a Go file can be unreadable, in a file CI compiles, in a file
// CI skips, and as a new file. None may produce a result.
func TestUnreadableGoFiles(t *testing.T) {
	bad := map[string]string{
		"empty":     "",
		"garbage":   "\x7fELF\x02\x01\x01\x00 not a Go file \xff\xfe",
		"truncated": "", // filled from the real file below
		"buildlog":  "# example.com/shipyardfixed/store\nstore/lock_windows.go:4:41: undefined: writeFile\n",
		"runlog":    "--- FAIL: TestSave (0.00s)\n    store_test.go:7: open: no such file\nFAIL\nexit status 1\n",
	}
	targets := []string{"store/store.go", "store/lock_windows.go", "store/new.go"}
	src := examples(t, "shipyard-fixed")
	for _, target := range targets {
		for name, content := range bad {
			t.Run(name+"/"+target, func(t *testing.T) {
				dir := copyDir(t, src)
				p := filepath.Join(dir, target)
				if name == "truncated" {
					orig, err := os.ReadFile(filepath.Join(src, "store", "store.go"))
					if err != nil {
						t.Fatal(err)
					}
					content = string(orig[:len(orig)*2/3])
				}
				if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
				for _, failOn := range []string{"warn", "none"} {
					r := runTool(t, nil, args(dir, append(fixedCI, "--fail-on", failOn)...)...)
					assertUntrusted(t, r, name+" "+target+" --fail-on "+failOn)
					if !strings.Contains(r.stderr, target) {
						t.Errorf("stderr doesn't name %s:\n%s", target, r.stderr)
					}
				}
			})
		}
	}
}

func TestBadConstraintLine(t *testing.T) {
	dir := copyDir(t, examples(t, "shipyard-fixed"))
	if err := os.WriteFile(filepath.Join(dir, "store", "lock_unix.go"),
		[]byte("//go:build unix &&\n\npackage store\n\nfunc lockDir(dir string) error { return nil }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertUntrusted(t, runTool(t, nil, args(dir, fixedCI...)...), "bad //go:build")
}

func TestUnreadableGoMod(t *testing.T) {
	bad := map[string]string{
		"empty":     "",
		"garbage":   "\x00\x01\x02\x03",
		"truncated": "module \"example.com/shipy",
		"cutgo":     "module example.com/shipyardfixed\n\ngo 1.",
		"buildlog":  "go: downloading example.com/x v1.0.0\ngo: example.com/x@v1.0.0: reading https://proxy.golang.org: 404 Not Found\n",
	}
	for name, content := range bad {
		t.Run(name, func(t *testing.T) {
			dir := copyDir(t, examples(t, "shipyard-fixed"))
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, failOn := range []string{"warn", "none"} {
				r := runTool(t, nil, args(dir, append(fixedCI, "--fail-on", failOn)...)...)
				assertUntrusted(t, r, "go.mod "+name)
			}
		})
	}
}

func TestBadCISpecs(t *testing.T) {
	dir := examples(t, "shipyard-fixed")
	for _, spec := range []string{"", " ", "linux", "linux/amd65", "\x01garbage", "linux/amd64,",
		"FAIL\texample.com/m [build failed]", "js/wasm,cgo"} {
		for _, failOn := range []string{"warn", "none"} {
			r := runTool(t, nil, "--dir", dir, "--ci", spec, "--fail-on", failOn)
			assertUntrusted(t, r, "--ci "+spec)
		}
	}
}

func TestNothingToCheck(t *testing.T) {
	empty := writeModule(t, map[string]string{"README.md": "no Go here\n"})
	assertUntrusted(t, runTool(t, nil, "--dir", empty), "module with no Go files")
	assertUntrusted(t, runTool(t, nil, "--dir", empty, "--fail-on", "none"), "module with no Go files, --fail-on none")
	assertUntrusted(t, runTool(t, nil, "--dir", filepath.Join(empty, "missing")), "missing dir")
	assertUntrusted(t, runTool(t, nil, "--dir", filepath.Join(empty, "README.md")), "dir is a file")
	noMod := t.TempDir()
	if err := os.WriteFile(filepath.Join(noMod, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertUntrusted(t, runTool(t, []string{"GO111MODULE=on", "GOFLAGS=-mod=mod", "GOWORK=off"}, "--dir", noMod), "no go.mod")
}

// fakeGo writes a go command that answers dist list with the real go and
// does something broken for everything else.
func fakeGo(t *testing.T, behavior string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake go command is a shell script")
	}
	real, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nif [ \"$1\" = tool ]; then exec " + real + " \"$@\"; fi\n" +
		"if [ \"$1 $2\" = \"list -m\" ]; then exec " + real + " \"$@\"; fi\n" + behavior + "\n"
	p := filepath.Join(t.TempDir(), "go")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBrokenGoCommand(t *testing.T) {
	cases := map[string]string{
		"fails":     "echo 'go: updates to go.mod needed' >&2; exit 1",
		"garbage":   "printf '\\x00\\x01 not json'",
		"truncated": "printf '{\"Dir\": \"/tmp\", \"GoFiles\": [\"a.go\"'",
		"buildlog":  "echo '# example.com/m'; echo 'a.go:1:1: expected package'; exit 1",
		"nodir":     "echo '{\"ImportPath\": \"x\"}'",
	}
	dir := examples(t, "shipyard-fixed")
	for name, behavior := range cases {
		t.Run(name, func(t *testing.T) {
			env := []string{"NEVERBUILT_GO=" + fakeGo(t, behavior)}
			for _, failOn := range []string{"warn", "none"} {
				r := runTool(t, env, args(dir, append(fixedCI, "--fail-on", failOn)...)...)
				assertUntrusted(t, r, "go list "+name)
			}
		})
	}
	// A go command that can't list ports at all.
	r := runTool(t, []string{"NEVERBUILT_GO=" + filepath.Join(t.TempDir(), "no-such-go")}, args(dir)...)
	assertUntrusted(t, r, "missing go command")
}

// A go list that works for the CI configs but breaks while compiling the
// skipped files must not turn into a pass.
func TestBrokenCompileStep(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake go command is a shell script")
	}
	real, _ := exec.LookPath("go")
	behavior := "for a in \"$@\"; do if [ \"$a\" = -export ]; then printf '{\"Dir\":'; exit 0; fi; done\nexec " + real + " \"$@\""
	env := []string{"NEVERBUILT_GO=" + fakeGo(t, behavior)}
	r := runTool(t, env, args(examples(t, "shipyard-fixed"), "--fail-on", "none")...)
	assertUntrusted(t, r, "compile step output cut off")
}

// go list output without export data is no proof that anything compiled.
func TestCompileWithoutExportData(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake go command is a shell script")
	}
	real, _ := exec.LookPath("go")
	behavior := "for a in \"$@\"; do if [ \"$a\" = -export ]; then " + real + " \"$@\" | grep -v '\"Export\"'; exit 0; fi; done\nexec " + real + " \"$@\""
	env := []string{"NEVERBUILT_GO=" + fakeGo(t, behavior)}
	r := runTool(t, env, args(examples(t, "shipyard-fixed"))...)
	if r.code != 1 || strings.Contains(r.stdout, "compiles for") {
		t.Fatalf("exit %d; claimed a compile without export data:\n%s%s", r.code, r.stdout, r.stderr)
	}
	mustContain(t, r, "couldn't confirm it compiles", "returned no compiled output")
}

func TestStandaloneGenerator(t *testing.T) {
	files := map[string]string{
		"lib.go": "package lib\n\n// Answer is used by the generator.\nconst Answer = 42\n",
		"gen.go": "//go:build gen\n\npackage main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(Answer) }\n",
	}
	dir := writeModule(t, files)
	r := runTool(t, nil, "--dir", dir)
	if r.code != 1 {
		t.Fatalf("exit %d:\n%s%s", r.code, r.stdout, r.stderr)
	}
	mustContain(t, r, "ERROR gen.go", "fails to compile for linux/amd64,gen", "undefined: Answer")

	files["gen.go"] = "//go:build gen\n\npackage main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(42) }\n"
	r = runTool(t, nil, "--dir", writeModule(t, files))
	mustContain(t, r, "WARN  gen.go", "compiles on its own for linux/amd64,gen, as go run gen.go would build it, but go build ./... for linux/amd64,gen fails")
	if strings.Contains(r.stdout, "also run CI with") {
		t.Errorf("suggested a CI config that would break go build ./...:\n%s", r.stdout)
	}
	if strings.Contains(r.stdout, "ERROR") {
		t.Errorf("healthy generator reported as broken:\n%s", r.stdout)
	}
}

func TestTestsOnlyFailure(t *testing.T) {
	dir := writeModule(t, map[string]string{
		"p.go":         "package p\n\nfunc F() int { return 1 }\n",
		"p_windows.go": "package p\n\nfunc G() int { return 2 }\n",
		"p_linux.go":   "package p\n\nfunc onlyLinux() int { return 3 }\n",
		"p_test.go":    "package p\n\nimport \"testing\"\n\nfunc TestF(t *testing.T) { _ = onlyLinux() }\n",
	})
	r := runTool(t, nil, "--dir", dir)
	mustContain(t, r, "WARN  p_windows.go", "compiles for windows/amd64, but its package's tests don't", "undefined: onlyLinux")
}

func otherOS() string {
	if runtime.GOOS == "freebsd" {
		return "netbsd"
	}
	return "freebsd"
}

func TestCgoForOtherPlatformIsNotAPass(t *testing.T) {
	goos := otherOS()
	dir := writeModule(t, map[string]string{
		"p.go":              "package p\n",
		"c_" + goos + ".go": "package p\n\n// int one(void) { return 1; }\nimport \"C\"\n\nfunc One() int { return int(C.one()) }\n",
	})
	for _, failOn := range []string{"warn", "error", "none"} {
		r := runTool(t, nil, "--dir", dir, "--fail-on", failOn)
		mustContain(t, r, "WARN  c_"+goos+".go", "cgo for another platform needs a C cross-compiler", "0 errors, 1 warning.")
		if strings.Contains(r.stdout, "All ") {
			t.Errorf("--fail-on %s: unchecked cgo file reported as clean:\n%s", failOn, r.stdout)
		}
	}
}

func TestSkippedDirsAndSymlinks(t *testing.T) {
	broken := "package x\n\nfunc F() { undefined() }\n"
	dir := writeModule(t, map[string]string{
		"a.go":                "package a\n",
		"testdata/x.go":       broken,
		"vendor/v/x.go":       broken,
		"_old/x.go":           broken,
		".hidden/x.go":        broken,
		"nested/go.mod":       "module example.com/nested\n\ngo 1.22\n",
		"nested/x_windows.go": broken,
		"_skip.go":            "not go at all",
	})
	assertClean(t, runTool(t, nil, "--dir", dir))

	if runtime.GOOS == "windows" {
		return
	}
	target := filepath.Join(t.TempDir(), "real_windows.go")
	if err := os.WriteFile(target, []byte("package a\n\nfunc W() { undefined() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link_windows.go")); err != nil {
		t.Fatal(err)
	}
	r := runTool(t, nil, "--dir", dir)
	mustContain(t, r, "ERROR link_windows.go", "undefined: undefined")
}

func TestCoveredBrokenFileIsCIsJob(t *testing.T) {
	// A file CI compiles is CI's to fail. neverbuilt says it is compiled by a
	// CI config, never that it compiles.
	dir := writeModule(t, map[string]string{
		"a.go": "package a\n\nfunc F() { undefined() }\n",
	})
	r := runTool(t, nil, "--dir", dir)
	assertClean(t, r)
	mustContain(t, r, "All 1 Go files are compiled by at least one CI config.")
}

// The tool's own module is a real program that CI builds on linux/amd64:
// it must stay quiet.
func TestOwnRepoIsQuiet(t *testing.T) {
	root, _ := filepath.Abs("..")
	r := runTool(t, nil, "--dir", root, "--ci", "linux/amd64")
	assertClean(t, r)
	mustContain(t, r, "All ")
}

func TestVersion(t *testing.T) {
	r := runTool(t, nil, "--version")
	if r.code != 0 || r.stdout != "neverbuilt 0.1.0\n" {
		t.Errorf("--version: exit %d, %q", r.code, r.stdout)
	}
}

const brokenLine = "\nvar x int = \"s\"\n"

// wantNotClean runs the tool on files plus a.go and checks the run isn't a
// pass under the default --fail-on, whatever it says.
func wantNotClean(t *testing.T, env []string, files map[string]string, extra ...string) result {
	t.Helper()
	if _, ok := files["a.go"]; !ok {
		files["a.go"] = "package p\n"
	}
	r := runTool(t, env, append([]string{"--dir", writeModule(t, files)}, extra...)...)
	if r.code == 0 || strings.Contains(r.stdout, "All ") || strings.Contains(r.stdout, "skipped on purpose") {
		t.Errorf("reported a pass (exit %d):\n%s%s", r.code, r.stdout, r.stderr)
	}
	return r
}

// Findings from an adversarial review, each once a false pass.
func TestReviewIgnoreTagNegatedOrOptional(t *testing.T) {
	r := wantNotClean(t, nil, map[string]string{"w.go": "//go:build windows && !ignore\n\npackage p\n" + brokenLine})
	mustContain(t, r, "ERROR w.go")
	r = wantNotClean(t, nil, map[string]string{"w.go": "//go:build windows || ignore\n\npackage p\n" + brokenLine})
	mustContain(t, r, "ERROR w.go")
}

func TestReviewBuildLinesGoIgnores(t *testing.T) {
	r := wantNotClean(t, nil, map[string]string{"w_windows.go": "// +build ignore\npackage p\n" + brokenLine})
	mustContain(t, r, "ERROR w_windows.go")
	r = wantNotClean(t, nil, map[string]string{"w_windows.go": "/*\nx\n*/ //go:build ignore\n\npackage p\n" + brokenLine})
	mustContain(t, r, "ERROR w_windows.go")
}

func TestReviewUncheckableIsAWarning(t *testing.T) {
	for _, line := range []string{"//go:build amd64.v3", "//go:build gccgo", "//go:build goexperiment.nosuchthing", "//go:build go1.999"} {
		r := wantNotClean(t, nil, map[string]string{"f.go": line + "\n\npackage p\n" + brokenLine})
		mustContain(t, r, "WARN  f.go", "not checked")
	}
	// A dotted custom tag is a tag, so the file is compiled.
	r := wantNotClean(t, nil, map[string]string{"f.go": "//go:build my.feature\n\npackage p\n" + brokenLine})
	mustContain(t, r, "ERROR f.go", "linux/amd64,my.feature")
}

func TestReviewArchLevelEnvDoesNotLeak(t *testing.T) {
	r := wantNotClean(t, []string{"GOAMD64=v3"}, map[string]string{"fast.go": "//go:build amd64.v3\n\npackage p\n" + brokenLine})
	mustContain(t, r, "WARN  fast.go", "architecture level amd64.v3")
}

func TestReviewGOOSAsTagRejected(t *testing.T) {
	dir := writeModule(t, map[string]string{"a.go": "package p\n"})
	assertUntrusted(t, runTool(t, nil, "--dir", dir, "--ci", "linux/amd64,windows"), "GOOS as a tag")
}

func TestReviewOtherPackageInDir(t *testing.T) {
	r := wantNotClean(t, nil, map[string]string{"a2.go": "package p\n", "b_windows.go": "package q\n\nfunc F() {}\n"})
	if strings.Contains(r.stdout, "compiles for windows/amd64") {
		t.Errorf("claimed a mixed-package build compiles:\n%s", r.stdout)
	}
	r = wantNotClean(t, nil, map[string]string{"gen.go": "//go:build windows\n\npackage main\n\nfunc main() {}\n"})
	if strings.Contains(r.stdout, "also run CI with") || !strings.Contains(r.stdout, "go build ./... for windows/amd64 fails") {
		t.Errorf("mixed main file handled wrong:\n%s", r.stdout)
	}
}

func TestReviewUntaggedMainInLibraryDir(t *testing.T) {
	r := wantNotClean(t, nil, map[string]string{"gen.go": "package main\n\nfunc main() {}\n" + brokenLine})
	mustContain(t, r, "ERROR gen.go")
}

func TestReviewPureGoErrorNextToCgo(t *testing.T) {
	files := map[string]string{
		"c.go":         "package p\n\nimport \"C\"\n",
		"w_windows.go": "package p\n" + brokenLine,
	}
	if runtime.GOOS == "windows" {
		t.Skip("needs a host that isn't windows")
	}
	r := wantNotClean(t, nil, files, "--fail-on", "error")
	mustContain(t, r, "ERROR w_windows.go")
}

func TestReviewGoErrorTextLooksLikeC(t *testing.T) {
	r := wantNotClean(t, nil, map[string]string{"w_windows.go": "package p\n\nvar x int = \"fatal error: no\"\n"}, "--fail-on", "error")
	mustContain(t, r, "ERROR w_windows.go")
}

func TestReviewManyTags(t *testing.T) {
	line := "//go:build a && b && !c && !d && !e && !f && !g && !h && !i && !j && !k"
	r := wantNotClean(t, nil, map[string]string{"f.go": line + "\n\npackage p\n" + brokenLine}, "--fail-on", "error")
	mustContain(t, r, "ERROR f.go", "linux/amd64,a,b")
}

func TestReviewTestOnlyDir(t *testing.T) {
	r := runTool(t, nil, "--dir", writeModule(t, map[string]string{
		"a.go":                "package p\n",
		"t/x_windows_test.go": "package t\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n",
	}))
	mustContain(t, r, "WARN  t/x_windows_test.go", "never compiled by CI; compiles for windows/amd64")
}

func TestReviewGoWorkWithTwoModules(t *testing.T) {
	dir := writeModule(t, map[string]string{
		"a.go":     "package p\n",
		"b/go.mod": "module example.com/b\n\ngo 1.22\n",
		"b/b.go":   "package b\n",
		"go.work":  "go 1.22\n\nuse (\n\t.\n\t./b\n)\n",
	})
	assertClean(t, runTool(t, nil, "--dir", dir))
}

func TestIgnoredTemplateIsSkipped(t *testing.T) {
	r := runTool(t, nil, "--dir", writeModule(t, map[string]string{
		"a.go":      "package p\n",
		"tmpl/x.go": "//go:build ignore\n\npackage {{.Pkg}}\n",
	}))
	assertClean(t, r)
	mustContain(t, r, "note  tmpl/x.go", "isn't valid Go", "the other 1 is skipped on purpose")
	// Without an ignore tag the same file can't be trusted.
	r = runTool(t, nil, "--dir", writeModule(t, map[string]string{
		"a.go":      "package p\n",
		"tmpl/x.go": "//go:build linux\n\npackage {{.Pkg}}\n",
	}))
	assertUntrusted(t, r, "unparseable file without ignore tag")
}
