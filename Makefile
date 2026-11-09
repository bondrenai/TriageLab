GO ?= go
export GOFLAGS := -trimpath

.PHONY: build fmt check test test-integration demo manifest
build:
	$(GO) build -o bin/triagelab ./cmd/triagelab
fmt:
	gofmt -w cmd internal
check:
	@test -z "$$(gofmt -l cmd internal)" || (gofmt -l cmd internal; exit 1)
	$(GO) vet ./...
	$(GO) test ./...
test:
	$(GO) test ./...
test-integration:
	TRIAGELAB_INTEGRATION=1 $(GO) test -count=1 -timeout 15m -v ./internal/investigation -run TestLiveInvestigation
manifest:
	$(GO) run ./cmd/triagelab manifest
demo:
	$(GO) run ./cmd/triagelab investigate --config triagelab.example.json --description-file examples/config-bug/issue.md --repo examples/config-bug
