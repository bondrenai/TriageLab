#!/usr/bin/env bash
# Install only into the explicitly named disposable kind cluster.
# TRIAGELAB_SANDBOX_CLASS=microvm selects the optional KVM-backed WorkerPool;
# create the cluster from deploy/kind-microvm.yaml first. See docs/development.md.
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ "$(uname -s)" != "Linux" ]]; then
  echo "Run this installation on the disposable Linux development VM." >&2
  exit 2
fi
sandbox_class="${TRIAGELAB_SANDBOX_CLASS:-gvisor}"
case "$sandbox_class" in
  gvisor)
    cli_config=deploy/kagent-cli.yaml
    ready_timeout=300s
    ;;
  microvm)
    cli_config=deploy/kagent-cli-microvm.yaml
    # A golden snapshot is a full guest boot plus a checkpoint.
    ready_timeout=600s
    ;;
  *)
    echo "TRIAGELAB_SANDBOX_CLASS must be gvisor or microvm." >&2
    exit 2
    ;;
esac
for tool in docker git kind kubectl helm kagent go; do
  command -v "$tool" >/dev/null || { echo "Missing prerequisite: $tool" >&2; exit 2; }
done
umask 077
kubeconfig=$(mktemp)
trap 'rm -f "$kubeconfig"' EXIT
kind get kubeconfig --name triagelab > "$kubeconfig"
export KUBECONFIG="$kubeconfig"
if ! existing_controller=$(kubectl --context kind-triagelab -n kagent \
  get deployment kagent-controller --ignore-not-found -o name); then
  echo "Cannot inspect the controller deployment; refusing to install." >&2
  exit 2
fi
if [[ -n "$existing_controller" ]]; then
  echo "This installer requires a fresh cluster without a kagent controller. Use scripts/install-fork-controller.sh to update an existing controller; a released controller can delete retained investigations." >&2
  exit 2
fi
if [[ -z "${OPENAI_API_KEY:-}" ]]; then
  echo "Set OPENAI_API_KEY for the kagent installer; its value is never sent to the fixture." >&2
  exit 2
fi
if [[ "$sandbox_class" == microvm ]]; then
  # Fail before installing anything when the node cannot host microVM workers.
  for node in $(kind get nodes --name triagelab); do
    docker exec "$node" test -c /dev/kvm || {
      echo "Node $node has no /dev/kvm. Recreate the cluster from deploy/kind-microvm.yaml on a KVM-capable Linux host." >&2
      exit 2
    }
    docker exec "$node" chmod 666 /dev/kvm
  done
fi
kagent --config "$cli_config" install --namespace kagent
bash scripts/install-fork-controller.sh
if [[ "$sandbox_class" == microvm ]]; then
  # Workers request the ate.dev/kvm device from atelet and stay Pending without it.
  kvm=""
  for _ in $(seq 30); do
    kvm="$(kubectl --context kind-triagelab get nodes \
      -o jsonpath='{.items[*].status.capacity.ate\.dev/kvm}')"
    [[ -n "$kvm" && "$kvm" != 0 ]] && break
    sleep 2
  done
  if [[ -z "$kvm" || "$kvm" == 0 ]]; then
    echo "No node advertises ate.dev/kvm, so microVM workers cannot be scheduled." >&2
    exit 1
  fi
  bash scripts/install-microvm-assets.sh
fi
go run ./cmd/triagelab manifest | kubectl --context kind-triagelab apply -f -
kubectl --context kind-triagelab -n kagent wait \
  --for=condition=Ready sandboxtemplate/triagelab-python --timeout="$ready_timeout"
kubectl --context kind-triagelab -n kagent wait \
  --for=condition=Ready agent/triagelab-investigator --timeout="$ready_timeout"
