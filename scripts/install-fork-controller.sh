#!/usr/bin/env bash
# Build the reviewed controller fork on the disposable Linux development host.
set -euo pipefail

if [[ "$(uname -s)" != "Linux" ]]; then
  echo "Run this controller build on the disposable Linux development VM." >&2
  exit 2
fi
for tool in docker git go kind kubectl; do
  command -v "$tool" >/dev/null || { echo "Missing prerequisite: $tool" >&2; exit 2; }
done

revision=048e1016d50b209dba01065d0d10dfd80b78fb41
image="localhost/triagelab-kagent-controller:$revision"
umask 077
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
kind get kubeconfig --name triagelab > "$scratch/kubeconfig"
kube=(kubectl --kubeconfig "$scratch/kubeconfig" --context kind-triagelab -n kagent)
"${kube[@]}" get deployment kagent-controller >/dev/null

git init --quiet "$scratch/source"
git -C "$scratch/source" remote add origin https://github.com/bondrenai/kagent.git
git -C "$scratch/source" fetch --depth=1 origin "$revision"
git -C "$scratch/source" checkout --quiet --detach FETCH_HEAD
if [[ "$(git -C "$scratch/source" rev-parse HEAD)" != "$revision" ]]; then
  echo "The controller checkout does not match the reviewed source pin." >&2
  exit 2
fi

mkdir "$scratch/image"
build_date=$(git -C "$scratch/source" show -s --format=%cs HEAD)
(
  cd "$scratch/source/go"
  CGO_ENABLED=0 GOFLAGS=-trimpath go build -buildvcs=false \
    -ldflags "-s -w -X github.com/kagent-dev/kagent/go/core/internal/version.Version=1.0.0 -X github.com/kagent-dev/kagent/go/core/internal/version.GitCommit=$revision -X github.com/kagent-dev/kagent/go/core/internal/version.BuildDate=$build_date -X github.com/kagent-dev/kagent/go/core/internal/version.SubstrateVersion=1.0.0" \
    -o "$scratch/image/app" ./core/cmd/controller/main.go
)

# Reuse the released 1.0.0 runtime filesystem; only the controller executable changes.
cat > "$scratch/image/Dockerfile" <<'EOF'
FROM ghcr.io/kagent-dev/kagent/controller@sha256:a85cc7452b403e879f592a094dfb3fb3f40b3552a2c1c91956184c7280850ed5
COPY --chown=65532:65532 app /app
EOF
docker build --pull=false \
  --label "org.opencontainers.image.revision=$revision" \
  --label "org.opencontainers.image.source=https://github.com/bondrenai/kagent" \
  -t "$image" "$scratch/image"
kind load docker-image "$image" --name triagelab
"${kube[@]}" patch deployment kagent-controller --type=strategic \
  --patch "{\"spec\":{\"template\":{\"spec\":{\"containers\":[{\"name\":\"controller\",\"image\":\"$image\",\"imagePullPolicy\":\"IfNotPresent\"}]}}}}"
"${kube[@]}" rollout status deployment/kagent-controller --timeout=300s
printf 'Installed controller source %s as %s\n' "$revision" "$image"
docker image inspect "$image" --format 'Local image ID: {{.Id}}'
