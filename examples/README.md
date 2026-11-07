# Examples

- `shipyard/` is a made-up module with mistakes that a Linux-only `go test ./...` never sees: a Windows file calling a helper a refactor renamed, an integration test using a renamed field, a `//go:build !widows` typo, a `_plan9.go` file that also says `//go:build linux`, and a generator marked `//go:build ignore`. Run `make demo-linux`.
- `shipyard-fixed/` is the same module with the mistakes fixed. Run `make demo-fixed`, which passes the CI matrix the first run suggested, and it exits 0.

Both are separate modules, so the repo's own `go vet ./...` and `go test ./...` don't build them.
