#!/usr/bin/env bash
# Install only into the explicitly named disposable kind cluster.
set -euo pipefail
cd "$(dirname "$0")/.."
for tool in kind kubectl helm kagent go; do
  command -v "$tool" >/dev/null || { echo "Missing prerequisite: $tool" >&2; exit 2; }
done
if [[ "$(kubectl config current-context)" != "kind-triagelab" ]]; then
  echo "Refusing to install outside context kind-triagelab. See docs/development.md." >&2
  exit 2
fi
if [[ -z "${OPENAI_API_KEY:-}" ]]; then
  echo "Set OPENAI_API_KEY for the kagent installer; its value is never sent to the fixture." >&2
  exit 2
fi
kagent --config deploy/kagent-cli.yaml install --namespace kagent
go run ./cmd/triagelab manifest | kubectl --context kind-triagelab apply -f -
kubectl --context kind-triagelab -n kagent wait \
  --for=condition=Ready sandboxtemplate/triagelab-python --timeout=300s
kubectl --context kind-triagelab -n kagent wait \
  --for=condition=Ready agent/triagelab-investigator --timeout=300s
