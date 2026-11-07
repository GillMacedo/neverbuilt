---
name: finding-never-compiled-files
description: Finds Go files that no CI build compiles because of build tags (//go:build integration, e2e, fuzz), GOOS or GOARCH file suffixes (_windows.go, _darwin.go, _arm64.go) or cgo, and compiles each one in a configuration that includes it to show whether it still builds. Use when CI is green but tagged integration tests or platform files may have rotted, when reviewing a refactor that renamed functions or fields, when a Windows or macOS build broke unexpectedly, or when deciding which GOOS, GOARCH and -tags a CI matrix needs.
---

# Finding Go files CI never compiles

The bundled tool needs Go 1.22 or newer. It runs `go list` and `go list -export`, which compile packages without linking or running anything. It never runs the project's code or tests.

## 1. Work out what CI really compiles

Read `.github/workflows/*.yml`, `.gitlab-ci.yml`, `Makefile`, `Taskfile.yml`, `.goreleaser.yaml` and any scripts they call. For every step that runs `go test`, `go vet` or `go build`, note:

- GOOS and GOARCH. A matrix over `runs-on` means one per runner: `ubuntu-latest` is linux/amd64, `macos-latest` is darwin/arm64, `windows-latest` is windows/amd64.
- The `-tags` value, including tags set through `GOFLAGS`.
- Whether cgo is on. Go turns cgo on for native builds when a C compiler is present, which is true on GitHub's Linux and macOS runners, unless `CGO_ENABLED=0` is set. Cross-compiling (GOOS or GOARCH set to something else) turns it off.
- Whether the step compiles tests. `go build` doesn't, and `go test` and `go vet` do. The tool assumes each configuration compiles tests, so leave out a config that only runs `go build` unless the user agrees.

Write each one as `GOOS/GOARCH[,tag...][,cgo]`, such as `linux/amd64,integration,cgo`. If you can't tell what CI runs, say so and ask the user, or use `linux/amd64` and say that's an assumption.

## 2. Run it

    go run "${CLAUDE_PLUGIN_ROOT}/scripts/neverbuilt.go" --dir MODULE_DIR --ci linux/amd64,cgo --ci linux/amd64,integration,cgo

Repeat `--ci` once per configuration. Add `--format json` to parse the result. If the project needs a non-default modfile or vendoring, set `GOFLAGS` as CI does; the tool passes the environment through, apart from GOOS, GOARCH and CGO_ENABLED.

## Exit codes

0 means every Go file is compiled by at least one `--ci` configuration, apart from files marked with an ignore tag. It doesn't mean those files compile: CI checks them. 1 means findings. 2 means bad usage or input the tool couldn't read, such as a Go file that doesn't parse or a `go list` failure. Treat 2 as unknown, never as a pass.

## Report

1. List each ERROR with its file and the compiler message, and say which build tags or platform hide it. These files are broken today, and CI can't see it.
2. For WARN "compiles for X", say the file is unchecked by CI and suggest adding X to the matrix, or a compile-only step such as `GOOS=windows go vet ./...` or `go test -tags integration -run '^$' ./...`.
3. Say plainly which WARN lines weren't verified: cgo for another platform, C code that doesn't compile on this machine, or a package that fails for other reasons.
4. A typo warning means a tag looks like a misspelled GOOS or GOARCH. Show the `//go:build` line, because the file is probably built on the wrong platforms.
5. Don't edit code or CI files unless the user asks.
