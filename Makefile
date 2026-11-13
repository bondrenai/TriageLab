GO ?= go
NPM ?= npm
HELM ?= helm
export GOFLAGS := -trimpath

# Release version stamped into the binary (main.version). Override with
# VERSION=v1.2.3; without git metadata it falls back to "dev".
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

# Linters run as pinned `go run module@version`, which keeps them out of go.mod
# and vendor/. Unlike the vendored build, they download from the module proxy.
STATICCHECK ?= honnef.co/go/tools/cmd/staticcheck@v0.8.2
GOVULNCHECK ?= golang.org/x/vuln/cmd/govulncheck@v1.9.0

# Static CLI binaries published to the GitHub Release (they do not embed the web UI).
DIST ?= dist
DIST_TARGETS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: build fmt check test race lint vulncheck dist test-integration test-pause-integration test-sandbox-integration demo manifest install-ui dev-ui build-ui check-ui install-docs dev-docs build-docs docs-generate docs-check chart-lint
build:
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/triagelab ./cmd/triagelab
dist:
	mkdir -p $(DIST)
	rm -f $(DIST)/triagelab_* $(DIST)/SHA256SUMS
	@set -e; for t in $(DIST_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -ldflags "$(LDFLAGS)" \
			-o $(DIST)/triagelab_$${os}_$${arch} ./cmd/triagelab; \
	done
	cd $(DIST) && shasum -a 256 triagelab_* > SHA256SUMS
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
# Separate from check: the race detector slows the password-hashing sign-in
# tests about tenfold, past the default per-package timeout on small runners.
race:
	$(GO) test -race -timeout 30m ./...
lint:
	$(GO) run $(STATICCHECK) ./...
vulncheck:
	$(GO) run $(GOVULNCHECK) ./...
test-integration:
	TRIAGELAB_INTEGRATION=1 $(GO) test -count=1 -timeout 75m -v ./internal/investigation -run TestLiveInvestigation
test-pause-integration:
	TRIAGELAB_PAUSE_INTEGRATION=1 $(GO) test -count=1 -timeout 20m -v ./internal/investigation -run '^TestLiveInvestigationPauseResume$$'
test-sandbox-integration:
	TRIAGELAB_SANDBOX_INTEGRATION=1 $(GO) test -count=1 -timeout 20m -v ./internal/investigation -run '^TestLiveSandboxBoundary$$'
manifest:
	$(GO) run ./cmd/triagelab manifest
demo:
	$(GO) run ./cmd/triagelab investigate --config triagelab.example.json --description-file examples/missing-orders/ticket.md --repo examples/missing-orders/repro
chart-lint:
	$(HELM) lint chart/triagelab --set token.value=lint-placeholder
	$(HELM) template chart/triagelab --set token.value=lint-placeholder > /dev/null
	$(HELM) template chart/triagelab --set token.secretName=lint-placeholder > /dev/null
	$(HELM) template chart/triagelab --include-crds --set token.secretName=lint-placeholder --set gitops.enabled=true --set 'gitops.secretNames[0]=ticket-token' > /dev/null
