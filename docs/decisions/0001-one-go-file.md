# 0001: One standard-library Go file

**Decision:** the tool is a single Go file, `plugin/scripts/neverbuilt.go`, using only the standard library.

**Why:** everyone who needs it already has Go, so `go run` is the whole install, with no npm package or release binaries. A single file also runs with `go run path/to/neverbuilt.go` from inside any other module, which is how the skill calls it from the plugin folder, where there is no go.mod.

**Alternatives:** `golang.org/x/tools/go/packages` would give typed results but adds a dependency and still calls `go list` underneath. An MCP server would give an agent the same command with more setup and nothing a script can't do.
