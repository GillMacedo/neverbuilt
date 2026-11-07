# neverbuilt

Claude Code skill and Go CLI that finds Go files no CI configuration compiles, because of build tags, GOOS or GOARCH file names, or cgo, and compiles each one in a configuration that includes it.

## Architecture
`plugin/scripts/neverbuilt.go` holds all the logic in one standard-library file, so `go run` needs no setup. `walk` finds and fully parses every Go file that `./...` could match. `listCovered` asks `go list -e ./...` which files each `--ci` configuration compiles. `findConfigs` searches ports, tag subsets and cgo with `go/build`'s `MatchFile` for configurations that include an uncovered file. `compile` runs `go list -e -export -test` and attributes compiler errors to files. `plugin/skills/finding-never-compiled-files/SKILL.md` tells Claude how to use it. See `docs/architecture.md`.

## Commands
- `make test`: gofmt, vet, unit tests and the integration tests in `tests/`
- `make lint`: plugin and marketplace manifest validation
- `make demo`: the two README examples on `examples/shipyard` and `examples/shipyard-fixed`

## Conventions
- Standard library only. Never run the code being checked; `go list` and `go list -export` are the only commands it runs.
- An input it can't read fully (a Go file that doesn't parse, a bad `//go:build` line, a `go.mod` or `go list` failure, `go list` output that isn't valid JSON or has no export data) exits 2 or becomes a finding, and never prints a clean summary.
- A file is only reported as broken when every configuration tried really compiled and failed. Anything not compiled is a warning, never a pass.
- Tests must not assume the host GOOS or GOARCH. Pick targets explicitly, or derive the "other" one from `runtime`.

## Do and don't
- Do keep README examples as real output from `make demo-linux` and `make demo-fixed`.
- Don't say a file compiles because CI covers it. CI checks covered files; the tool only says they are compiled by a CI config.

## Definition of done
`make test`, `make lint` and `make demo` pass, and `ROADMAP.md`, `README.md` and docs are updated when relevant.
