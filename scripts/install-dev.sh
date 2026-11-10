#!/usr/bin/env bash
# Install only into the explicitly named disposable kind cluster.
# TRIAGELAB_SANDBOX_CLASS=microvm selects the optional KVM-backed WorkerPool;
# create the cluster from deploy/kind-microvm.yaml first. See docs/development.md.
set -euo pipefail
cd "$(dirname "$0")/.."
sandbox_class="${TRIAGELAB_SANDBOX_CLASS:-gvisor}"
case "$sandbox_class" in
  gvisor)
    cli_config=deploy/kagent-cli.yaml
    tools=(kind kubectl helm kagent go)
    ready_timeout=300s
    ;;
  microvm)
    cli_config=deploy/kagent-cli-microvm.yaml
    tools=(kind kubectl helm kagent go docker git)
    # A golden snapshot is a full guest boot plus a checkpoint.
    ready_timeout=600s
    ;;
  *)
    echo "TRIAGELAB_SANDBOX_CLASS must be gvisor or microvm." >&2
    exit 2
    ;;
esac
for tool in "${tools[@]}"; do
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
