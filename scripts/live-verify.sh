#!/usr/bin/env bash
# Run one opt-in live check against the disposable kind-triagelab deployment
# that scripts/install-dev.sh installed. It opens its own loopback port-forward
# and mints a one-hour caller token, then removes both. `investigation` and
# `pause` call the model provider; `sandbox` does not. A TRIAGELAB_CONFIG, if
# set, must keep the default loopback endpoint and TRIAGELAB_TOKEN variable.
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ "$(uname -s)" != "Linux" ]]; then
  echo "Run the live checks on the disposable Linux development VM." >&2
  exit 2
fi
case "${1:-}" in
  sandbox) target=test-sandbox-integration ;;
  investigation) target=test-integration ;;
  pause) target=test-pause-integration ;;
  *)
    echo "Usage: $0 sandbox|investigation|pause" >&2
    exit 2
    ;;
esac
for tool in go kind kubectl make timeout; do
  command -v "$tool" >/dev/null || { echo "Missing prerequisite: $tool" >&2; exit 2; }
done
umask 077
scratch=$(mktemp -d)
forward=""
cleanup() {
  if [[ -n "$forward" ]]; then
    kill "$forward" 2>/dev/null || true
  fi
  rm -rf "$scratch"
}
trap cleanup EXIT
kind get kubeconfig --name triagelab > "$scratch/kubeconfig"
kube=(kubectl --kubeconfig "$scratch/kubeconfig" --context kind-triagelab -n kagent)
if (exec 3<>/dev/tcp/127.0.0.1/8083) 2>/dev/null; then
  echo "127.0.0.1:8083 is already in use. Stop that port-forward, or run 'make $target' against it directly." >&2
  exit 2
fi
"${kube[@]}" port-forward --address 127.0.0.1 svc/kagent-controller 8083:8083 \
  > "$scratch/port-forward.log" 2>&1 &
forward=$!
if ! timeout 30 bash -c 'until (exec 3<>/dev/tcp/127.0.0.1/8083) 2>/dev/null; do sleep 1; done'; then
  echo "The controller port-forward did not become reachable." >&2
  cat "$scratch/port-forward.log" >&2
  exit 1
fi
TRIAGELAB_TOKEN="$("${kube[@]}" create token triagelab --audience=kagent-invocation --duration=1h)"
if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then
  echo "::add-mask::$TRIAGELAB_TOKEN"
fi
export TRIAGELAB_TOKEN
make "$target"
