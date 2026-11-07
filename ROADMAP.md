# Roadmap

## Next
- Read GOOS, GOARCH and `-tags` from `.github/workflows/*.yml` matrices, labeled as a guess the user confirms
- A GitHub Action example that runs `neverbuilt` with the same matrix as the test job
- Check file name suffixes for typos too, such as `_widnows.go`

## Later
- Search tag sets across a whole package, so a file whose helpers need another tag is compiled with both
- `--verify-ci` to also compile the declared configurations, for a single "every file compiles somewhere" answer
- Use a C cross-compiler for cgo builds when one is configured
