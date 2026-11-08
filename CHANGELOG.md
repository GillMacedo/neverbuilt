# Changelog

All notable changes are documented here, in [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format. This project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.2.0] - 2026-11-08

### Added
- Add structured CI configs to JSON output (#8)

## [0.1.0] - 2026-11-07

### Added
- Lists Go files that no `--ci` configuration (GOOS, GOARCH, build tags and cgo) compiles, using `go list -e ./...` for each configuration.
- Compiles each of those files, with tests, in the simplest configuration that includes it through `go list -e -export -test`, and tries up to four configurations before reporting a file as broken.
- Errors for files that fail to compile. Warnings for files that compile but no CI config covers, packages whose tests fail, unverifiable cgo builds, contradictory constraints and tags that look like misspelled GOOS or GOARCH names. Notes for files with an ignore tag or that need gccgo, a newer Go, a GOEXPERIMENT or an architecture level.
- `package main` generators in library directories are compiled on their own, as `go run` would build them.
- `--ignore-tag`, `--no-compile`, `--format json`, `--fail-on`, and exit 2 for any input it can't trust.
- `finding-never-compiled-files` skill and plugin manifest.
