# TriageLab container image: static CLI plus the built private web UI.
# The server only ever listens on a loopback address; in Kubernetes the UI is
# reached through `kubectl port-forward`, never a Service or Ingress.
#
# Multi-arch (linux/amd64, linux/arm64) without emulation: both build stages
# run on the build host's platform ($BUILDPLATFORM) and cross-compile, and the
# final stage has no RUN instruction, so no foreign-architecture code executes.
# Base images are pinned by multi-arch index digest; refresh tag and digest
# together (docker buildx imagetools inspect IMAGE:TAG).

# Build the private web UI (versions match .github/workflows/ci.yml). The UI
# imports the recorded verification example outside web/, so that one file is
# copied at the relative path the TypeScript import expects. The output is
# architecture independent.
FROM --platform=$BUILDPLATFORM node:24.18.1-alpine@sha256:f70403e87646dc51b45295f4b8b70cdad0b63d2297c4c9899119b03f7af7a6b3 AS web
WORKDIR /app
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
COPY docs/verification/agent-demo.json /docs/verification/agent-demo.json
RUN npm run build

# Build the statically linked CLI, cross-compiled for the target platform. The
# OpenTelemetry SDK is vendored, so the image build never reaches the network.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
COPY vendor/ vendor/
COPY cmd/ cmd/
COPY internal/ internal/
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -mod=vendor -ldflags "-X main.version=$VERSION" -o /out/triagelab ./cmd/triagelab \
    && mkdir -p /out/app

# Busybox wget in Alpine backs the loopback liveness probe; there is no shell
# access to investigation data because the workspace runs unprivileged. The
# unprivileged identity is numeric (matching the chart's runAsUser/runAsGroup),
# so no adduser step is needed; /app is owned by it.
FROM alpine:3.22@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f
COPY --from=build /out/triagelab /usr/local/bin/triagelab
COPY --from=build --chown=10001:10001 /out/app /app
COPY --from=web /app/dist /app/web/dist
WORKDIR /app
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/triagelab"]
CMD ["serve"]
