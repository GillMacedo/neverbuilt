# neverbuilt: find Go files your CI never compiles

[![CI](https://github.com/GillMacedo/neverbuilt/actions/workflows/ci.yml/badge.svg)](https://github.com/GillMacedo/neverbuilt/actions/workflows/ci.yml) [![MIT license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

**A Claude Code skill and Go CLI for Go developers and CI maintainers. It lists the Go files that none of your CI builds compile, because of build tags like `//go:build integration`, platform files like `_windows.go`, or cgo, and then compiles each one in a configuration that includes it to show whether it still builds.**

Go drops a file from the build when its constraints don't match, and says nothing. `go test ./...` on Linux never type-checks `lock_windows.go`, and without `-tags integration` it never sees your integration tests. A refactor renames a function or a field, CI stays green, and the break shows up weeks later on someone's Mac, in the release build for Windows, or when someone finally runs the integration suite. `neverbuilt` finds those files and tells you which ones are already broken.

## What you can ask Claude

- "CI is green, but are our integration tests behind build tags still compiling?"
- "Which Go files does our GitHub Actions matrix never build?"
- "I renamed a struct field. Did I break any `_windows.go` or `_darwin.go` files?"
- "Which GOOS, GOARCH and -tags should CI add so every file is compiled at least once?"

## Install

As a Claude Code plugin:

    claude plugin marketplace add GillMacedo/neverbuilt
    claude plugin install neverbuilt@neverbuilt-marketplace

Or run it directly with Go 1.22 or newer. It has no dependencies:

    go run github.com/GillMacedo/neverbuilt/plugin/scripts@latest --dir . --ci linux/amd64 --ci linux/amd64,integration

## Example

`examples/shipyard` is a made-up module with mistakes that a Linux-only `go test ./...` can't see: a Windows file still calling a helper that a refactor renamed, an integration test using a renamed field, a `//go:build !widows` typo, and a `_plan9.go` file that also says `//go:build linux`. CI for this example runs `go test ./...` on linux/amd64 only:

    $ make demo-linux
    neverbuilt examples/shipyard: 12 Go files; CI configs: linux/amd64
      ERROR clock/label_windows.go
            never compiled by CI, and fails to compile for windows/amd64
            clock/label_windows.go:3:7: label redeclared in this block
      ERROR store/lock_windows.go
            never compiled by CI, and fails to compile for windows/amd64
            store/lock_windows.go:4:41: undefined: writeFile
      ERROR store/store_integration_test.go
            never compiled by CI, and fails to compile for linux/amd64,integration
            store/store_integration_test.go:9:49: unknown field Size in struct literal of type Record
      WARN  clock/label_other.go
            build tag "widows" looks like a misspelling of "windows"
            //go:build !widows
      WARN  legacy/legacy_plan9.go
            no supported GOOS, GOARCH, tag set or cgo setting compiles this file
            //go:build linux
      note  tools/gen.go
            skipped: needs tag ignore, which marks files that aren't meant to compile
      To compile every file, also run CI with: linux/amd64,integration  windows/amd64
      3 errors, 2 warnings. 7 of 12 Go files are compiled by a CI config.

The `label redeclared` error comes from the typo: `!widows` is true on Windows, so both label files are built there.

`examples/shipyard-fixed` is the same module with the mistakes fixed. With the CI matrix that the first run suggested, it stays quiet and exits 0:

    $ make demo-fixed
    neverbuilt examples/shipyard-fixed: 11 Go files; CI configs: linux/amd64  linux/amd64,integration  windows/amd64
      note  tools/gen.go
            skipped: needs tag ignore, which marks files that aren't meant to compile
      10 of 11 Go files are compiled by a CI config; the other 1 is skipped on purpose (notes above).

## Usage

    neverbuilt [--dir DIR] [--ci GOOS/GOARCH[,tag...][,cgo]]... [--ignore-tag T,...]
               [--no-compile] [--format text|json] [--fail-on warn|error|none]

1. **What CI compiles.** For each `--ci` configuration, it runs `go list -e ./...` in `DIR` with that GOOS, GOARCH, `-tags` and `CGO_ENABLED`, and collects every Go file and test file in the result. The default is one configuration, `linux/amd64`. Write `cgo` among the tags to set `CGO_ENABLED=1`; without it, cgo is off.
2. **What's on disk.** It walks `DIR` the way `./...` does, skipping `testdata`, `vendor`, nested modules, and names starting with `.` or `_`. Every file is parsed in full.
3. **The gap.** For each file that no configuration compiles, it looks for the simplest configuration that includes it: the fewest extra tags, then a port from your `--ci` list, then common ports, then the rest, with cgo off before on. It compiles that configuration's packages, with tests, using `go list -e -export -test`, which runs the real compiler without linking or running anything. If that fails, it tries up to three more configurations before it reports the file as broken.

- **`--ignore-tag`** marks files that aren't meant to compile, such as generators with `//go:build ignore`. They're listed as notes. The default is `ignore,tools`.
- **`--no-compile`** only lists the files CI skips.
- **`--format json`** prints the same findings for scripts and agents.
- **`--fail-on`** sets the lowest severity that exits 1: `warn` by default, or `error` or `none`.

Exit 0 means every Go file is compiled by at least one `--ci` configuration, apart from ignored files. Exit 1 means findings. Exit 2 means bad usage or an input it couldn't trust: a Go file that's empty, cut off or not Go, a bad `//go:build` line, an unreadable `go.mod`, a `--ci` value that isn't a real port, or `go list` failing or printing something that isn't valid output. Exit 2 ignores `--fail-on`, and it prints no summary.

## What it reports

| Finding | Severity | Meaning |
|---|---|---|
| `fails to compile for X` | ERROR | No CI config compiles the file, and in X the compiler reports errors in the file itself |
| `compiles for X` | WARN | No CI config compiles the file. It compiles in X today, but nothing keeps it that way |
| `compiles for X, but its package's tests don't` | WARN | The file builds, but the package's tests fail to compile in X |
| `couldn't confirm it compiles` | WARN | The package fails in X because of another file or a dependency, so this file couldn't be judged |
| `wasn't compiled, because cgo for another platform needs a C cross-compiler` | WARN | Only a cgo build for another platform includes the file. It isn't checked |
| `its C code doesn't compile on this machine` | WARN | A cgo build failed in C, usually a missing header or library. The Go code wasn't judged |
| `no supported GOOS, GOARCH, tag set or cgo setting compiles this file` | WARN | The constraints contradict each other or the file name, so the file is dead code |
| `looks like a misspelling of` | WARN | A tag in `//go:build` is one or two letters from a GOOS or GOARCH, like `widows` or `darwn`, or differs only in case, like `Linux` |
| `not checked: may need ...` | WARN | The file needs gccgo, a newer Go release, a GOEXPERIMENT or an architecture level like `amd64.v3`, which the tool doesn't set |
| `skipped: needs tag ignore` | note | Only a build with an `--ignore-tag` tag includes the file. A file that says `!ignore` or `ignore \|\| windows` is checked like any other |
| `skipped: isn't valid Go, and its //go:build line needs an ignore tag` | note | A code template that go never builds |

A file that CI does compile is CI's job. If it's broken, your CI is already red, so `neverbuilt` doesn't compile it again.

## How it compares

- **`go vet` and golangci-lint** check the files of one build configuration per run. golangci-lint takes one `--build-tags` set, so you run it once per set and have to know which sets you need. `go vet`'s `buildtag` check validates the syntax of `//go:build` lines, not which files they leave out.
- **A loop over `GOOS=... go vet ./...`** catches platform files if you list the right platforms and tags. `neverbuilt` works out which configurations are missing from the files themselves, and compiles only those.
- **`go list -f '{{.IgnoredGoFiles}}'`** lists the files that one configuration leaves out. It doesn't combine several configurations or try to compile what's left.

I searched GitHub and the web for tools that report files no CI configuration compiles, or that compile every build tag combination, and found none. Keyword search can miss projects.

## Limits

- **It trusts your `--ci` list.** It can't read your CI files. The skill tells Claude to work them out, and the result is only as good as that list.
- **It assumes CI's default architecture levels.** GOAMD64, GOARM and similar variables are cleared for every `go list` run, so a file that needs `amd64.v3` is reported as not checked.
- **It assumes each config compiles tests**, as `go test` and `go vet` do. A CI step that only runs `go build` compiles fewer files than the tool counts.
- **cgo for other platforms isn't compiled.** That needs a C cross-compiler. Those files are reported as unchecked. cgo for the host platform is compiled if a C compiler is installed.
- **The simplest configuration may not be the one you use.** A file is called broken only if every configuration it tried (up to four) fails. If a file needs several tags at once, or a tag and a platform the search doesn't reach, add that configuration yourself.
- **Tags are only searched from the file's own `//go:build` line.** If a package needs a tag set on other files too, such as a shared test helper, the first try may fail and the next one may not include it.
- **The typo check is a heuristic.** It skips names shorter than five letters, names used in three or more files, names set by a `--ci` config, and known TinyGo and App Engine tags such as `wasi`, `tinygo` and `appengine`.
- A file with a valid package clause that was cut off between two declarations still parses, so it can't be told apart from a short file.
- A `//line` directive changes the file name in compiler errors, so an error in such a file is reported as an unconfirmed package (WARN), not an ERROR.
- The `ignore` directive in `go.mod` (Go 1.25) isn't read. Files in ignored directories are reported as never compiled.
- Every file is parsed, so a Go file that isn't valid Go stops the run with exit 2, unless its `//go:build` line needs an ignore tag.

## FAQ

**Why doesn't Go warn about files excluded by build tags?** Excluding files is what build constraints are for, so `go build` stays quiet. The cost is that excluded files are never type-checked.

**How do I compile integration tests without running them?** `go test -tags integration -run '^$' ./...` compiles the test binaries and runs no tests. `neverbuilt` tells you which tags and platforms need a step like that.

**How do I check `_windows.go` files on Linux?** `GOOS=windows GOARCH=amd64 go vet ./...` type-checks them without cgo. `neverbuilt` finds every such platform in one run.

**Does it run my code or tests?** No. `go list -export` compiles packages to export data, without linking or running anything. cgo builds do run the C compiler on your C code.

**Does it need network access?** No, as long as the module's dependencies are already in the module cache. If one isn't, `go list` may try to download it, and an offline failure shows up as an unconfirmed package.

**Is it an MCP server?** No. It's a skill with one standard-library Go file, so `go run` is the whole install.

## Tested on

- Unit tests for `--ci` parsing, constraint reading, configuration search, port order, the typo heuristic and standalone generator detection.
- Integration tests in `tests/` that run the built tool on `examples/shipyard`, its fixed copy, and small modules written by the tests: a broken `package main` generator in a library directory and a healthy one, a package whose tests fail on one platform, a cgo file for a platform other than the test machine, a symlinked file, skipped directories, and the tool's own repo as a healthy project that must stay quiet. Each Go file input was replaced with an empty file, garbage, a file cut off partway, `go build` failure output and `go test` failure output, in a file CI compiles, a file it skips and a new file. `go.mod` was replaced with an empty file, garbage, two cut-off versions and `go` download error output, and `--ci` got empty, garbage and build-log values. A fake `go` command checked failing, garbage and cut-off `go list` output, and `go list -export` output with no export data. All of these exit 2, or report a finding, and none prints a clean result, with or without `--fail-on none`.
- An independent review that tried to make it report a false pass. It found ten problems, all fixed with tests in `tests/`: an `ignore` tag under `!` or `||` was treated as "skipped on purpose", files needing gccgo or `amd64.v3` gave exit 0, `GOAMD64=v3` in the environment made a file count as covered, `--ci linux/amd64,windows` was accepted, a `package main` file in a library directory was called "compiles" when `go build` fails there, a skipped cgo build turned a pure Go type error into a warning, a Go error message containing `fatal error:` was taken for a C error, and a constraint with more than ten tags was called unbuildable.
- Go 1.22.12 and Go 1.27.3 on linux/arm64, with `-race`. The CI workflow runs the same tests on linux/amd64 with Go 1.22 and the latest stable Go.

Run on the default branches of real projects on 2026-11-07, with the dependencies downloaded:

- **gocql/gocql**, with the tags its CI uses (`unit`, `integration`, `cassandra`, `ccm`): two real errors. `fuzz.go`, behind the `gofuzz` tag, fails with `too many arguments in call to newFramer`. `host_source_gen.go`, a `package main` generator behind `genhostinfo`, can't build in its directory, and `go run -tags genhostinfo host_source_gen.go` fails the same way.
- **minio/minio-go**: `rdma_test.go` fails with `undefined: maxRDMABufferSize` when built with `-tags rdma` and cgo off. The cgo build couldn't be checked because its C header isn't installed here, so that file is a WARN, not an ERROR. It also reports `functional_tests.go`, a `package main` test harness behind the `mint` tag, as an ERROR, because it can't build inside the library's directory. That's true of the file as it sits in the repo, but minio's mint setup probably builds it from a copy, so that one is likely intended.
- **gorilla/websocket**: with `-tags appengine`, `mask_safe.go` compiles, but the package's tests don't (`undefined: wordSize`).
- **prometheus/node_exporter** with only linux/amd64: no errors, and its cgo collectors for other platforms are reported as unchecked. An earlier version reported them as errors, because it compiled them with cgo off. That was fixed and is covered by a test.
- **fatih/color, go-chi/chi, go-sql-driver/mysql, spf13/afero and spf13/pflag**, with linux, darwin and windows on amd64: quiet, exit 0.
- **fsnotify/fsnotify, mattn/go-isatty, sirupsen/logrus, urfave/cli, IBM/sarama, jackc/pgx, etcd-io/bbolt and others**: warnings only, for BSD, Solaris, plan9, js/wasm and tag-gated files that compile but no CI config covers. Early runs gave false typo warnings for `wasip2`, `wasi`, `unit` and `fips`. The heuristic was tightened, and tests cover those names.

Not tested: Windows or macOS as the machine running the tool, gccgo, cgo cross-compilers, and use in a fresh Claude session.

## Contributing

Contributions are welcome, from people and from people working with AI agents. See [CONTRIBUTING.md](CONTRIBUTING.md). Good first issues are [listed here](https://github.com/GillMacedo/neverbuilt/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22).

### How pull requests work

1. **Open a PR.** Fork the repo, work on a branch, and keep it to one change. Write a title that says what changes for users, because it becomes the changelog line: `feat:` goes under Added and `fix:` under Fixed.
2. **Automatic check.** Code changes need tests, and `make test` must pass. Changes over 30 lines need a linked open issue (`Closes #<number>`), and a PR can have at most 800 changed lines. If something is missing, a comment tells you what to fix.
3. **Review.** Claude reviews each PR automatically. Code PRs also get a security review and are tested in a sandbox. The review then merges the PR or asks for changes, and a small problem may become a follow-up issue instead. A PR with no new commit 14 days after a change request is closed, and you can reopen it. PRs that change CI, agent settings, npm or Python dependencies, vendored code or build files can't be merged this way, and a comment says so. The same goes for anything the security review doesn't clear.
4. **Release.** A merge that changes what users install becomes a new version within about two hours, with a GitHub Release and, if the project has one, its npm package. Docs and CI changes wait for the next release.

## AI disclosure

This project was largely generated with Claude Code and reviewed by its maintainer.

## About me

I'm Giulliano Macedo, and I build developer tools at GoTech Solutions. Follow along or get in touch:

- GitHub: [GillMacedo](https://github.com/GillMacedo)
- LinkedIn: [giulliano](https://www.linkedin.com/in/giulliano/)
- X: [@giumacedo](https://x.com/giumacedo)

If `neverbuilt` saved you some prompts, a star helps others find it.

---

Built by [Giulliano Macedo](https://github.com/GillMacedo), GoTech Solutions · [LinkedIn](https://www.linkedin.com/in/giulliano/) · [X](https://x.com/giumacedo)
