.PHONY: test lint demo demo-linux demo-fixed tool

test:
	test -z "$$(gofmt -l plugin tests examples)"
	go vet ./...
	cd examples/shipyard-fixed && go vet ./...
	go test -race -count=1 ./...

lint:
	claude plugin validate ./plugin --strict
	claude plugin validate . --strict

TOOL := bin/neverbuilt

tool:
	@go build -o $(TOOL) ./plugin/scripts

demo: demo-linux demo-fixed

# examples/shipyard has five mistakes on purpose, so exit 1 is the expected result.
demo-linux: tool
	@$(TOOL) --dir examples/shipyard || test $$? -eq 1

# The same project with the mistakes fixed and the CI matrix the first run suggested.
# It must stay quiet and exit 0.
demo-fixed: tool
	@$(TOOL) --dir examples/shipyard-fixed --ci linux/amd64 --ci linux/amd64,integration --ci windows/amd64
