GO ?= go
NPM ?= npm
HELM ?= helm
export GOFLAGS := -trimpath

# Release version stamped into the binary (main.version). Override with
# VERSION=v1.2.3; without git metadata it falls back to "dev".
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

# Linters run as pinned `go run module@version` so go.mod stays dependency-free.
# Both versions are the newest that still build with the pinned Go 1.25
# toolchain; newer releases require Go 1.26.
STATICCHECK ?= honnef.co/go/tools/cmd/staticcheck@v0.7.0
GOVULNCHECK ?= golang.org/x/vuln/cmd/govulncheck@v1.7.0

# Static CLI binaries published to the GitHub Release (they do not embed the web UI).
DIST ?= dist
DIST_TARGETS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: build fmt check test lint vulncheck dist test-integration demo manifest install-ui dev-ui build-ui check-ui chart-lint
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
fmt:
	gofmt -w cmd internal
check:
	@test -z "$$(gofmt -l cmd internal)" || (gofmt -l cmd internal; exit 1)
	$(GO) vet ./...
	$(GO) test -race ./...
test:
	$(GO) test -race ./...
lint:
	$(GO) run $(STATICCHECK) ./...
vulncheck:
	$(GO) run $(GOVULNCHECK) ./...
test-integration:
	TRIAGELAB_INTEGRATION=1 $(GO) test -count=1 -timeout 75m -v ./internal/investigation -run TestLiveInvestigation
manifest:
	$(GO) run ./cmd/triagelab manifest
demo:
	$(GO) run ./cmd/triagelab investigate --config triagelab.example.json --description-file examples/config-bug/issue.md --repo examples/config-bug
chart-lint:
	$(HELM) lint chart/triagelab --set token.value=lint-placeholder
	$(HELM) template chart/triagelab --set token.value=lint-placeholder > /dev/null
	$(HELM) template chart/triagelab --set token.secretName=lint-placeholder > /dev/null
