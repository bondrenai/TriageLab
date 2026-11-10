GO ?= go
NPM ?= npm
HELM ?= helm
export GOFLAGS := -trimpath

.PHONY: build fmt check test test-integration test-pause-integration demo manifest install-ui dev-ui build-ui check-ui install-docs dev-docs build-docs docs-generate docs-check chart-lint
build:
	$(GO) build -o bin/triagelab ./cmd/triagelab
install-ui:
	$(NPM) --prefix web ci
dev-ui:
	$(NPM) --prefix web run dev
build-ui:
	$(NPM) --prefix web run build
check-ui:
	$(NPM) --prefix web run check
install-docs:
	$(NPM) --prefix docs-site ci
dev-docs:
	$(NPM) --prefix docs-site run dev
build-docs:
	$(NPM) --prefix docs-site run build
docs-generate:
	$(GO) run ./cmd/triagelab docs --out docs-site/src/content/docs/reference
docs-check:
	$(GO) run ./cmd/triagelab docs --check docs-site/src/content/docs/reference
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
test-pause-integration:
	TRIAGELAB_PAUSE_INTEGRATION=1 $(GO) test -count=1 -timeout 20m -v ./internal/investigation -run '^TestLiveInvestigationPauseResume$$'
manifest:
	$(GO) run ./cmd/triagelab manifest
demo:
	$(GO) run ./cmd/triagelab investigate --config triagelab.example.json --description-file examples/config-bug/issue.md --repo examples/config-bug
chart-lint:
	$(HELM) lint chart/triagelab --set token.value=lint-placeholder
	$(HELM) template chart/triagelab --set token.value=lint-placeholder > /dev/null
	$(HELM) template chart/triagelab --set token.secretName=lint-placeholder > /dev/null
