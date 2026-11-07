# Architecture

    parse flags --> go tool dist list (ports) --> go list -m (module ok?)
      --> walk DIR, parse every .go file in full (any failure: exit 2)
      --> for each --ci config: go list -e ./... --> files CI compiles
      --> for each other file:
            only builds with an ignore tag include it --> note
            findConfigs (MatchFile over tag subsets x ports x cgo, up to 4)
              none               --> unbuildable warning, or a not-checked warning for gccgo, newer Go, GOEXPERIMENT, arch levels
            compile rounds: group files by current candidate, go list -e -export -test once per group
              file compiles      --> warning "compiles for X"
              fails or skipped   --> next candidate, then error or warning
      --> typo check on every //go:build tag
      --> text or JSON --> exit code

- **The go command decides what CI compiles.** Coverage comes from `go list` with each configuration's GOOS, GOARCH, `-tags` and `CGO_ENABLED`, not from the tool's own constraint logic. The rest of the environment, such as `GOFLAGS` and `GOWORK`, is passed through.
- **The walk mirrors `./...`.** It skips `testdata`, `vendor`, nested modules and names starting with `.` or `_`, follows symlinked files but not symlinked directories, and parses every file with `go/parser`, so an empty, cut-off or garbage file stops the run.
- **Candidate search uses `go/build`.** `MatchFile` applies file name suffixes, `//go:build`, `unix` and release tags. It doesn't exclude files that import "C" when cgo is off, so the tool does that itself. Tags come only from the file's own constraint, and never include `cgo`, GOOS, GOARCH, release, GOEXPERIMENT or architecture level names.
- **Compiling is `go list -export -test`.** It runs the real compiler on each package and its test variants and reports errors in `Error` and `DepsErrors`, without linking. A package with no error and no export data counts as not compiled. Errors are matched to files by their `file:line:col:` prefix or the `Pos` field.
- **Generators are compiled alone.** A `package main` file that a configuration builds next to another package's files is passed to `go list` by file name, like `go run file.go`. If that compiles, the finding says `go build ./...` would still fail there, and the configuration isn't suggested for CI. Files `go list` puts in `InvalidGoFiles` never count as covered.
- **cgo is handled carefully.** cgo builds go to the host port when it matches. Other cgo builds are not compiled. A C compiler error (gcc's `file:line:col: error:` form, or a missing C compiler) makes that attempt unverifiable, so it can't turn into an ERROR or a pass. A skipped cgo build only softens errors another file could explain, like `undefined:`; a type mismatch stays an ERROR.
- **The environment is passed through, minus the platform.** GOOS, GOARCH, CGO_ENABLED and the architecture level variables (GOAMD64, GOARM and others) are set or cleared for each run.

Key choices are in `decisions/`.
