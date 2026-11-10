#!/usr/bin/env bash
# Stage the microVM guest assets (Cloud Hypervisor, virtiofsd, Kata kernel and
# rootfs) and the cluster-wide "microvm" SandboxConfig into the disposable kind
# cluster. The pinned Helm charts install neither. Substrate's own installer
# owns the asset versions, SHA-256 checks, staging, and the SandboxConfig, so
# this runs that installer from the release the charts and worker image use.
# It mirrors kagent rc3's scripts/kind/setup-microvm.sh; see docs/upstream.md.
set -euo pipefail
cd "$(dirname "$0")/.."
substrate_tag=v1.0.0
substrate_commit=563df06f6be751c101539986896c17c55aa22a9d
for tool in git go docker kubectl; do
  command -v "$tool" >/dev/null || { echo "Missing prerequisite: $tool" >&2; exit 2; }
done
if [[ "$(kubectl config current-context)" != "kind-triagelab" ]]; then
  echo "Refusing to install outside context kind-triagelab. See docs/development.md." >&2
  exit 2
fi
# The installer verifies cached downloads against its pinned digests on reuse.
asset_cache="${TRIAGELAB_MICROVM_ASSET_CACHE:-$PWD/.tools/microvm-assets}"
mkdir -p "$asset_cache"
source_dir="$(mktemp -d "${TMPDIR:-/var/tmp}/triagelab-substrate.XXXXXX")"
trap 'rm -rf "$source_dir"' EXIT
git clone --quiet --depth 1 --branch "$substrate_tag" --single-branch \
  https://github.com/kagent-dev/substrate.git "$source_dir"
if [[ "$(git -C "$source_dir" rev-parse HEAD)" != "$substrate_commit" ]]; then
  echo "Substrate $substrate_tag no longer resolves to the reviewed commit $substrate_commit." >&2
  exit 1
fi
ln -s "$asset_cache" "$source_dir/bin"
go -C "$source_dir" run -mod=readonly ./cmd/ate-setup \
  --kind --kind-cluster-name triagelab --context kind-triagelab \
  deploy sandboxconfig --sandbox-classes=microvm
