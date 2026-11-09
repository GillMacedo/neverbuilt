package main

import (
	"go/build/constraint"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testPorts() map[string]portInfo {
	return map[string]portInfo{
		"linux/amd64":   {GOOS: "linux", GOARCH: "amd64", CgoSupported: true, FirstClass: true},
		"linux/arm64":   {GOOS: "linux", GOARCH: "arm64", CgoSupported: true, FirstClass: true},
		"windows/amd64": {GOOS: "windows", GOARCH: "amd64", CgoSupported: true, FirstClass: true},
		"darwin/arm64":  {GOOS: "darwin", GOARCH: "arm64", CgoSupported: true, FirstClass: true},
		"js/wasm":       {GOOS: "js", GOARCH: "wasm"},
		"plan9/amd64":   {GOOS: "plan9", GOARCH: "amd64"},
		"android/arm64": {GOOS: "android", GOARCH: "arm64", CgoSupported: true},
		"freebsd/386":   {GOOS: "freebsd", GOARCH: "386", CgoSupported: true},
		"freebsd/amd64": {GOOS: "freebsd", GOARCH: "amd64", CgoSupported: true},
	}
}

func TestParseSpec(t *testing.T) {
	ports := testPorts()
	good := map[string]config{
		"linux/amd64":             {GOOS: "linux", GOARCH: "amd64", Tags: []string{}},
		" linux/amd64 ":           {GOOS: "linux", GOARCH: "amd64", Tags: []string{}},
		"linux/amd64,integration": {GOOS: "linux", GOARCH: "amd64", Tags: []string{"integration"}},
		"linux/amd64,cgo,e2e,a":   {GOOS: "linux", GOARCH: "amd64", Tags: []string{"a", "e2e"}, Cgo: true},
		"windows/amd64,x,x":       {GOOS: "windows", GOARCH: "amd64", Tags: []string{"x"}},
		"linux/amd64,my.feature":  {GOOS: "linux", GOARCH: "amd64", Tags: []string{"my.feature"}},
	}
	for in, want := range good {
		got, err := parseSpec(in, ports)
		if err != nil {
			t.Errorf("parseSpec(%q): %v", in, err)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("parseSpec(%q) = %+v, want %+v", in, got, want)
		}
	}
	bad := []string{"", "   ", "linux", "linux/", "/amd64", "linux/amd65", "plan10/amd64",
		"linux/amd64,", "linux/amd64,,x", "linux/amd64,a b", "linux/amd64,-x", "js/wasm,cgo",
		"go build failed: exit status 1", "\x00\x01",
		"linux/amd64,windows", "linux/amd64,unix", "linux/amd64,go1.21", "linux/amd64,goexperiment.foo",
		"linux/amd64,amd64.v3", "linux/amd64,gccgo"}
	for _, in := range bad {
		if _, err := parseSpec(in, ports); err == nil {
			t.Errorf("parseSpec(%q) succeeded, want an error", in)
		}
	}
}

func TestConfigString(t *testing.T) {
	c := config{GOOS: "linux", GOARCH: "amd64", Tags: []string{"a", "b"}, Cgo: true}
	if got := c.String(); got != "linux/amd64,a,b,cgo" {
		t.Errorf("String() = %q", got)
	}
}

func TestTypoHint(t *testing.T) {
	known := knownNames(testPorts())
	cases := []struct {
		id   string
		uses int
		want string
	}{
		{"widows", 1, "windows"},
		{"darwn", 1, "darwin"},
		{"freebds", 1, "freebsd"},
		{"Linux", 50, "linux"},
		{"Windows", 1, "windows"},
		// Real names that must stay quiet.
		{"linux", 1, ""},
		{"unit", 1, ""},      // too short to judge
		{"fips", 1, ""},      // too short to judge
		{"wasip2", 1, ""},    // differs from wasip1 only in a digit
		{"wasi", 1, ""},      // TinyGo
		{"tinygo", 1, ""},    // TinyGo
		{"appengine", 1, ""}, // App Engine
		{"integration", 1, ""},
		{"widows", 3, ""}, // used by many files: a real tag
		{"linuxx", 120, ""},
	}
	for _, c := range cases {
		if got := typoHint(c.id, known, c.uses); got != c.want {
			t.Errorf("typoHint(%q, %d) = %q, want %q", c.id, c.uses, got, c.want)
		}
	}
}

func TestSubsets(t *testing.T) {
	got := subsets([]string{"a", "b"}, nil)
	want := [][]string{nil, {"a"}, {"b"}, {"a", "b"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("subsets = %v, want %v", got, want)
	}
	many := make([]string, 12)
	for i := range many {
		many[i] = string(rune('a' + i))
	}
	if n := len(subsets(many, nil)); n != 15 {
		t.Errorf("subsets of 12 tags = %d sets, want 15", n)
	}
}

func writeFile(t *testing.T, dir, name, src string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadGoFile(t *testing.T) {
	dir := t.TempDir()
	ok := map[string]string{
		"plain.go":  "package p\n",
		"tagged.go": "//go:build (linux || darwin) && !integration\n\npackage p\n",
		"plus.go":   "// +build linux,amd64 windows\n\npackage p\n",
		"cgo.go":    "package p\n\nimport \"C\"\n",
	}
	for name, src := range ok {
		f, msg := readGoFile(writeFile(t, dir, name, src))
		if msg != "" {
			t.Errorf("%s: %s", name, msg)
			continue
		}
		switch name {
		case "tagged.go":
			if !reflect.DeepEqual(f.Idents, []string{"darwin", "integration", "linux"}) {
				t.Errorf("tagged.go idents = %v", f.Idents)
			}
		case "plus.go":
			if f.Expr == nil || !reflect.DeepEqual(f.Idents, []string{"amd64", "linux", "windows"}) {
				t.Errorf("plus.go idents = %v", f.Idents)
			}
		case "cgo.go":
			if !f.ImportsC || !f.UsesCgo {
				t.Errorf("cgo.go: ImportsC=%v UsesCgo=%v", f.ImportsC, f.UsesCgo)
			}
		}
	}
	bad := map[string]string{
		"empty.go":     "",
		"blank.go":     "\n\n  \n",
		"garbage.go":   "\x00\x01\x02 not go",
		"truncated.go": "package p\n\nfunc F() int {\n\treturn ",
		"buildlog.go":  "# example.com/m/p\np/p.go:3:9: undefined: x\n",
		"badbuild.go":  "//go:build linux &&\n\npackage p\n",
		"twobuild.go":  "//go:build linux\n//go:build windows\n\npackage p\n",
		"badparen.go":  "//go:build (linux\n\npackage p\n",
	}
	for name, src := range bad {
		if _, msg := readGoFile(writeFile(t, dir, name, src)); msg == "" {
			t.Errorf("%s: read as valid, want an error", name)
		}
	}
}

func TestPortOrder(t *testing.T) {
	ports := testPorts()
	order := portOrder([]config{{GOOS: "windows", GOARCH: "amd64"}}, ports)
	var names []string
	for _, p := range order {
		names = append(names, p.GOOS+"/"+p.GOARCH)
	}
	got := strings.Join(names, " ")
	if !strings.HasPrefix(got, "windows/amd64 linux/amd64 linux/arm64 darwin/arm64") {
		t.Errorf("order starts wrong: %s", got)
	}
	if !strings.HasSuffix(got, "android/arm64") {
		t.Errorf("android should come last: %s", got)
	}
	if strings.Index(got, "freebsd/amd64") > strings.Index(got, "freebsd/386") {
		t.Errorf("amd64 should come before 386: %s", got)
	}
}

func TestFindConfigs(t *testing.T) {
	dir := t.TempDir()
	ports := testPorts()
	known := knownNames(ports)
	order := portOrder([]config{{GOOS: "linux", GOARCH: "amd64"}}, ports)
	cases := map[string]string{
		"x_windows.go":   "package p\n",
		"integ_test.go":  "//go:build integration\n\npackage p\n",
		"both.go":        "//go:build windows && e2e\n\npackage p\n",
		"js.go":          "//go:build js\n\npackage p\n",
		"never_plan9.go": "//go:build linux\n\npackage p\n",
		"neg.go":         "//go:build !linux && !windows && !darwin && !freebsd && !android && !plan9\n\npackage p\n",
	}
	want := map[string]string{
		"x_windows.go":   "windows/amd64",
		"integ_test.go":  "linux/amd64,integration",
		"both.go":        "windows/amd64,e2e",
		"js.go":          "js/wasm",
		"never_plan9.go": "",
		"neg.go":         "js/wasm",
	}
	for name, src := range cases {
		f, msg := readGoFile(writeFile(t, dir, name, src))
		if msg != "" {
			t.Fatal(msg)
		}
		f.Abs = filepath.Join(dir, name)
		got := ""
		if c := findConfigs(f, known, order, maxTries, false, ports, nil); len(c) > 0 {
			got = c[0].String()
		}
		if got != want[name] {
			t.Errorf("%s: first config %q, want %q", name, got, want[name])
		}
	}
}

func TestFindConfigsCgoFile(t *testing.T) {
	dir := t.TempDir()
	ports := testPorts()
	known := knownNames(ports)
	order := portOrder([]config{{GOOS: "linux", GOARCH: "amd64"}}, ports)
	f, msg := readGoFile(writeFile(t, dir, "c_freebsd.go", "package p\n\nimport \"C\"\n"))
	if msg != "" {
		t.Fatal(msg)
	}
	f.Abs = filepath.Join(dir, "c_freebsd.go")
	c := findConfigs(f, known, order, maxTries, true, ports, nil)
	if len(c) == 0 || !c[0].Cgo || c[0].GOOS != "freebsd" {
		t.Errorf("cgo file configs = %v, want freebsd with cgo first", c)
	}
}

func TestSpecialNeed(t *testing.T) {
	cases := map[string]bool{
		"//go:build gccgo":              true,
		"//go:build go1.99":             true,
		"//go:build goexperiment.arena": true,
		"//go:build amd64.v3":           true,
		"//go:build my.feature":         false,
		"//go:build linux":              false,
		"//go:build go1.1":              false,
	}
	dir := t.TempDir()
	for line, want := range cases {
		f, msg := readGoFile(writeFile(t, dir, "f.go", line+"\n\npackage p\n"))
		if msg != "" {
			t.Fatal(msg)
		}
		if got := specialNeed(f) != ""; got != want {
			t.Errorf("specialNeed(%s) = %v, want %v", line, got, want)
		}
	}
}

func TestPosRE(t *testing.T) {
	m := posRE.FindStringSubmatch("p/p_windows.go:3:23: undefined: x")
	if m == nil || m[1] != "p/p_windows.go" || m[4] != "undefined: x" {
		t.Errorf("posRE = %q", m)
	}
	if posRE.MatchString("no required module provides package x") {
		t.Error("posRE matched a line with no position")
	}
}

func TestUsageErrors(t *testing.T) {
	var out, errb strings.Builder
	cases := [][]string{
		{"--format", "yaml"},
		{"--fail-on", "maybe"},
		{"stray"},
		{"--nope"},
	}
	for _, args := range cases {
		out.Reset()
		errb.Reset()
		if code := run(args, &out, &errb); code != exitUnusable {
			t.Errorf("run(%v) = %d, want %d", args, code, exitUnusable)
		}
		if out.Len() != 0 {
			t.Errorf("run(%v) printed to stdout: %q", args, out.String())
		}
	}
}

func TestPositives(t *testing.T) {
	x, err := constraint.Parse("//go:build a && !(b || !c) && (d || !e)")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	positives(x, false, got)
	want := map[string]bool{"a": true, "c": true, "d": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("positives = %v, want %v", got, want)
	}
}

func TestCErrorRE(t *testing.T) {
	for _, l := range []string{
		"rdma.go:17:11: fatal error: miniocpp/c_api.h: No such file or directory",
		"x.go:3:2: error: unknown type name 'foo'",
		"cgo: C compiler \"gcc\" not found: exec: \"gcc\": executable file not found in $PATH",
		"compilation terminated.",
	} {
		if !cErrorRE.MatchString(l) {
			t.Errorf("not seen as a C error: %s", l)
		}
	}
	for _, l := range []string{
		`c.go:5:13: cannot use "fatal error: no" (untyped string constant) as int value in variable declaration`,
		"a.go:3:9: undefined: error",
	} {
		if cErrorRE.MatchString(l) {
			t.Errorf("Go error seen as a C error: %s", l)
		}
	}
}

func TestDefinite(t *testing.T) {
	if definite([]string{"a.go:1:2: undefined: x", "a.go:2:2: c.F undefined (type *T has no field or method F)", "a.go:9:2: too many errors"}) {
		t.Error("undefined names could come from a skipped cgo file")
	}
	if !definite([]string{"a.go:1:2: undefined: x", `a.go:3:9: cannot use "s" (untyped string constant) as int value`}) {
		t.Error("a type mismatch can't be fixed by another file")
	}
}

func TestHeaderNeedsIgnore(t *testing.T) {
	ignore := []string{"ignore", "tools"}
	cases := map[string]bool{
		"//go:build ignore\n\npackage {{.Pkg}}\n":             true,
		"// Template.\n//go:build ignore && linux\npackage x": true,
		"//go:build !ignore\n\npackage {{.Pkg}}\n":            false,
		"//go:build ignore || linux\n\npackage {{\n":          false,
		"//go:build linux\n\npackage {{\n":                    false,
		"package {{.Pkg}}\n//go:build ignore\n":               false,
		"":                                                    false,
	}
	for src, want := range cases {
		if got := headerNeedsIgnore([]byte(src), ignore); got != want {
			t.Errorf("headerNeedsIgnore(%q) = %v, want %v", src, got, want)
		}
	}
}
