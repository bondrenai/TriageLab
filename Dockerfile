# TriageLab container image: static CLI plus the built private web UI.
# The server only ever listens on a loopback address; in Kubernetes the UI is
# reached through `kubectl port-forward`, never a Service or Ingress.

# Build the private web UI (versions match .github/workflows/ci.yml). The UI
# imports the recorded verification example outside web/, so that one file is
# copied at the relative path the TypeScript import expects.
FROM node:24.18.1-alpine AS web
WORKDIR /app
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
COPY docs/verification/missing-orders-demo.json /docs/verification/missing-orders-demo.json
RUN npm run build

# Build the statically linked CLI. The OpenTelemetry SDK is vendored, so the
# image build never reaches the network.
FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY vendor/ vendor/
COPY cmd/ cmd/
COPY internal/ internal/
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
RUN go build -mod=vendor -o /out/triagelab ./cmd/triagelab

# Busybox wget in Alpine backs the loopback liveness probe; there is no shell
# access to investigation data because the workspace runs unprivileged.
FROM alpine:3.22
RUN addgroup -S -g 10001 triagelab \
    && adduser -S -u 10001 -G triagelab -h /app triagelab
COPY --from=build /out/triagelab /usr/local/bin/triagelab
COPY --from=web /app/dist /app/web/dist
WORKDIR /app
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/triagelab"]
CMD ["serve"]
