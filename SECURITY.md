# Security

Report vulnerabilities through [GitHub's private vulnerability reporting](https://github.com/GillMacedo/neverbuilt/security/advisories/new), not in a public issue.

`neverbuilt` runs `go list` and `go list -export` on the module you point it at. Compiling with cgo runs the C compiler on the module's C code, as `go build` would. It never links or runs the code it checks. Please report privately if a crafted module could make it do anything else. A missed file or a wrong finding is a normal bug.
