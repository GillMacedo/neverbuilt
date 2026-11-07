# 0002: Compile with go list -export, and never call a skipped build a pass

**Decision:** files CI doesn't compile are checked with `go list -e -export -test` in a configuration that includes them. A file is an ERROR only when every configuration tried really compiled and reported errors in that file. Anything that wasn't compiled, or failed for reasons outside the file, is a WARN.

**Why:** `go list -export` runs the real compiler for the target GOOS and GOARCH, including test packages, without linking. It needs no C cross-compiler for pure Go code and runs nothing. Type-checking with `go/types` would miss compiler-only errors and duplicate what the go command already does.

**Why the strict rule:** the first real run on prometheus/node_exporter reported cgo-only collectors as broken because they were compiled with cgo off. Trying the cgo build next, and treating a build that can't run here as unverified, removed those false errors without hiding real ones.

**Alternatives:** `go vet` per configuration also type-checks, but adds analyzer findings that aren't build failures. `go test -run '^$'` links test binaries, which fails for cross-compiled cgo and is slower.
