# Development and real integration setup

Use a dedicated disposable Linux VM for Kubernetes, image pulls, builds, and test
runs. Editing and reading source can stay on a laptop. An initial allocation of
4 vCPUs and 16 GiB RAM is reasonable for this small development stack; increase
only if measurement shows pressure. Set a short automatic shutdown deadline,
save reports, then delete the VM and its disk when finished. TriageLab itself
does not provision or bill cloud resources.

The setup below is a **private, single-user development environment**. Do not
install it in a production cluster or publish its controller port. The released
kagent controller defaults to a no-op application authorizer. Even with the
service-account verifier enabled, the development authentication mode still has
other identity paths. An authenticated gateway and a real application authorizer
are prerequisites for shared or exposed use. See [SECURITY.md](../SECURITY.md).

To run the same single-user workspace as one pod inside this dedicated cluster
instead of on the VM, use the [public Helm chart](helm.md); its access model is
unchanged (loopback listener, `kubectl port-forward` only).

## Prerequisites on the VM

- Linux with Docker or a compatible kind provider. Docker is used to host kind;
  its socket is never mounted into an investigator or reproduction sandbox.
- Go 1.27.3, Git, `kubectl`, Helm 3, and kind 0.33.0. The included Python fixture
  runs inside the reproduction sandbox, not on the development host.
- kagent CLI **1.0.0**, matching the charts and runtime images. The installation
  script builds the reviewed controller fork from source on this Linux host.
- A provider account/key for the real agent. The example uses the upstream
  `gpt-6.1-sol` model through the Responses API, with bounded investigation limits. Model calls incur provider charges.
- Enough free disk for Kubernetes and Substrate images/snapshots (30 GiB free
  is a useful starting allowance).

Install the released kagent CLI binary. The controller fork is built separately
by the installation script. For Linux amd64:

```sh
mkdir -p .tools
curl -fL https://github.com/kagent-dev/kagent/releases/download/v1.0.0/kagent-linux-amd64 \
  -o .tools/kagent
echo 'c1a0a37767c4c2526e6d42b7016cfc11e69e82e321f6b84c1499d539d0dd864d  .tools/kagent' \
  | sha256sum -c -
chmod +x .tools/kagent
export PATH="$PWD/.tools:$PATH"
```

For other architectures, use the matching release asset and verify its published
checksum. Never substitute a different kagent version without rechecking the
interfaces in [upstream.md](upstream.md).

## Install the dedicated cluster

Run from the repository root **on the VM**:

```sh
kind create cluster --name triagelab --image kindest/node:v1.35.0 \
  --config deploy/kind.yaml --wait 120s
go build -o bin/triagelab ./cmd/triagelab
```

Set `OPENAI_API_KEY` in the VM shell using your secret manager or a hidden shell
prompt, then `export OPENAI_API_KEY`. Do not put it in a manifest, command history, repository, or report.
Then:

```sh
bash scripts/install-dev.sh
unset OPENAI_API_KEY
```

This selects only the explicitly named `triagelab` Kind cluster, without using
an ambient Kubernetes context. It uses kagent's installer for the pinned Substrate Helm distribution,
its pod certificate controller, Atelet, the development PostgreSQL database,
RustFS snapshot store, and kagent. It then builds controller commit
`048e1016d50b209dba01065d0d10dfd80b78fb41` from
[`bondrenai/kagent`](https://github.com/bondrenai/kagent/tree/048e1016d50b209dba01065d0d10dfd80b78fb41),
loads the image into Kind, and waits for its rollout before installing TriageLab's
Agent and SandboxTemplate. The source commit and local image ID are printed for
the verification record. There is no published fork image to pull.

The installer refuses an existing kagent controller before making cluster
changes. It is for fresh clusters only: temporarily running the released
controller would allow its expiration worker to delete retained investigations.

The installation also configures Substrate's ingress
identity, controller access grants, and required preview gates. TriageLab adds
only ordinary kagent resources, rendered by `triagelab manifest`. The script
requires Linux and selects only `kind-triagelab`.

The patched controller is the only forked component. Its runtime filesystem uses
the digest-pinned 1.0.0 base; the investigator runtime, guest image, and Substrate
keep their released versions. The helper reuses the host's Go build cache and
does not push an image or change a registry. To reinstall only the pinned
controller after a Helm install or upgrade, run on the VM:

```sh
bash scripts/install-fork-controller.sh
```

The chart still describes the released controller image. Do not run a chart
install, upgrade, or rollback that replaces the fork while investigations are
retained: even a short interval with the released controller can delete them,
and reinstalling the fork afterward cannot recover them. Close retained
investigations before changing the deployment through an upstream chart
operation. Keep the helper's pinned image loaded in the single-node Kind
cluster. For another cluster, publish a reviewed image and pin that deployment
explicitly before enabling resumable investigations.

The values set `config.sessions.idleTtl: 0s`. Waiting investigator Sessions can
therefore survive weeks without activity, while kagent still suspends idle
runtimes automatically. TriageLab must explicitly delete completed and closed
Sessions. Retained Sandbox files and Session state depend on keeping PostgreSQL
and the snapshot store.

The rendered `triagelab-tools` ClusterIP Service sends investigator MCP calls to
the same controller through a separate hostname. Keep that hostname distinct
from kagent's control-plane URL: kagent 1.0.0 replaces credentials at the control-plane
origin with an actor token, which has no sandbox ownership. The tools Service
preserves the short-lived caller token and the existing owner checks. It is
internal only and selects the `kagent` Helm release's controller.

Rendering is read-only and can be reviewed first:

```sh
bin/triagelab manifest > /tmp/triagelab-resources.json
kagent --config deploy/kagent-cli.yaml install --dry-run \
  --output-dir /tmp/triagelab-install-review
```

The exported upstream installation is a recipe; its script does not provision
all prerequisites that the normal installer provisions. Use the normal installer
for the development quickstart. After CRDs exist, server-side schema checking
is available without applying a change:

```sh
kubectl --context kind-triagelab apply --dry-run=server \
  -f /tmp/triagelab-resources.json
```

The default template contains Python 3.12 and standard Linux tools. It permits
no outbound connections. Use a reviewed digest-pinned image with additional
dependencies preinstalled for a different problem:

```sh
bin/triagelab manifest \
  --workload-image 'your-registry/tools@sha256:YOUR_VERIFIED_DIGEST' \
  > /tmp/triagelab-resources.json
```

Replace the digest placeholder before running; the renderer rejects invalid or
tag-only images. Additional sandbox templates — including
`SandboxTemplate.spec.egressPolicyRefs` — are a separate, explicit operator
decision, made either by manifest or through the workspace's Sandboxes and
Egress editors. No destination is inferred from issue content.

## Run the genuine demonstration

In a VM terminal, keep the controller reachable only on loopback:

```sh
kubectl --context kind-triagelab -n kagent port-forward \
  --address 127.0.0.1 svc/kagent-controller 8083:8083
```

In another VM terminal, mint a short-lived invocation credential:

```sh
export TRIAGELAB_TOKEN="$(kubectl --context kind-triagelab -n kagent \
  create token triagelab --audience=kagent-invocation --duration=1h)"
bin/triagelab investigate \
  --description-file examples/config-bug/issue.md \
  --repo examples/config-bug
unset TRIAGELAB_TOKEN
```

Run the CLI on the VM to keep compute remote. If using the CLI on your laptop,
connect its loopback endpoint through an SSH local port forward; never bind the
kagent endpoint to `0.0.0.0`. The CLI sends the selected case and files to kagent,
so use only material you are authorized to disclose to that model provider.

The investigator should inspect the fixture, run the failing application, and
perform a verification experiment with corrected configuration. Success requires
real process records, failure output, and evidence references in the final JSON
and Markdown. Model output alone is not proof of execution.

## Check and troubleshoot

Run tests, lint, and formatting on the VM:

```sh
make check
```

Inspect template and agent readiness before diagnosing a model problem:

```sh
kubectl --context kind-triagelab -n kagent get agents,sandboxtemplates,workerpools
kubectl --context kind-triagelab -n kagent describe agent triagelab-investigator
kubectl --context kind-triagelab -n kagent describe sandboxtemplate triagelab-python
kubectl --context kind-triagelab -n ate-system get pods
```

An empty or failed readiness condition usually indicates image access, Substrate
runtime capacity, certificate, object-store, or database setup. A denied external
DNS lookup inside a fixture is expected. A token must use the
`kagent-invocation` audience; refresh expired tokens before a run. Provider
authentication failures require correcting the controller-side Secret, never
putting the provider key into the sandbox.

If the CLI uploads successfully but the investigator receives `Sandbox access
denied`, verify that `RemoteMCPServer/triagelab-sandbox-api` uses the
`triagelab-tools` hostname with `tokenPassthrough: true` and that the CLI uses
a verified service-account token for the `kagent-invocation` audience. Using the
default controller hostname changes the outgoing identity to an actor token.
Keep `config.runtimeDefaults.tokenPropagation.enabled` set to `false` so the Go
ADK runtime forwards the token only to tools with explicit passthrough.

kagent's active Sandbox TTL expires even if the CLI disappears. An opted-in
Sandbox is retained only after suspension completes; resuming it starts a fresh
bounded active TTL. Ordinary interrupted runs still explicitly delete their
sandbox, while a successful pause keeps the same Sandbox and Session until resume
or close. A process-kill acknowledgment is not
proof it exited; the CLI observes completion and records cleanup failures. If a
controller outage prevents cleanup, retain the report's IDs and retry against
the same controller once it recovers.

The opt-in pause/resume test requires this disposable deployment with a real
model credential and the patched controller:

```sh
TRIAGELAB_CONFIG=/absolute/path/to/triagelab.json \
  TRIAGELAB_REPORT_DIR=/absolute/path/to/private-test-reports make test-pause-integration
```

It starts and pauses a real investigation, exits that process, resumes from a
new process, verifies the same sandbox/session and retained/new file bytes,
pauses again, then closes and verifies deletion. The first round's evidence
archive must remain byte-identical. `TRIAGELAB_PAUSE_RESUME_TOKEN` can supply a
refreshed caller token for the resume/close processes; keep it out of shell
history and logs. This checks real restoration and continuation, not weeks of
elapsed uptime. The fork's separate retention integration test covers passing
the original sandbox expiry deadline.

### Inspecting telemetry export locally

The OpenTelemetry SDK exports only when a telemetry configuration is supplied.
On the VM, run a throwaway collector on loopback and point a run at it:

```sh
docker run --rm -p 127.0.0.1:4318:4318 otel/opentelemetry-collector:0.165.0
TRIAGELAB_OTLP_HEADERS= triagelab investigate --telemetry triagelab.telemetry.example.json ...
```

`triagelab.telemetry.example.json` targets `http://127.0.0.1:4318`. The
collector's default config logs received traces, metrics, and logs to stdout,
which is enough to eyeball run spans (`investigation.run` and its phases),
kagent RPC spans, HTTP server spans, and the run/cleanup metrics. For a
collector that requires authentication, put the header lines in the
environment variable named by `headers_env` (never in the file). The unit
tests assert the exported OTLP payloads without a live collector; live export
is exercised only to the extent recorded in docs/verification.md.

## Optional microVM backend

The recorded live microVM check used the earlier kagent 1.0.0-rc3 pin.
The current 1.0.0 pin needs a separate live microVM verification.

Substrate's `microvm` sandbox class runs each actor as a KVM guest (Kata
Containers on Cloud Hypervisor) instead of under gVisor. TriageLab needs no new
runtime implementation: kagent takes the class from the WorkerPool that a
template references. The host, the kind node, and the install differ.

- **Host.** Linux with a usable `/dev/kvm`: bare metal, or a VM with nested
  virtualization enabled. Many cloud machine types and organization policies
  disable nested virtualization, and Apple Silicon before M3 cannot provide it.
- **Cluster.** Create it from `deploy/kind-microvm.yaml`, which is `kind.yaml`
  plus a `/dev/kvm` mount on the node.
- **Install.** `TRIAGELAB_SANDBOX_CLASS=microvm` makes the install script use
  `deploy/kagent-cli-microvm.yaml`, which gives `kagent-default` the `microvm`
  class, the `microvm` SandboxConfig reference, and the matching
  `ateworker-microvm:v1.0.0` image. It then runs
  `scripts/install-microvm-assets.sh`.

```sh
kind create cluster --name triagelab --image kindest/node:v1.35.0 \
  --config deploy/kind-microvm.yaml --wait 120s
TRIAGELAB_SANDBOX_CLASS=microvm bash scripts/install-dev.sh
```

Neither pinned chart installs the cluster-wide `microvm` SandboxConfig or the
guest assets it names. The assets script runs Substrate's own installer from
the pinned `v1.0.0` commit, as kagent rc3 does in its own CI. That installer
downloads the Kata and Cloud Hypervisor releases from GitHub, verifies their
pinned SHA-256 digests, stages them in the in-cluster RustFS bucket through a
Docker `aws-cli` container, and applies the SandboxConfig. It needs Docker,
`git`, and network access, and caches the downloads under `.tools/`.

The investigator runtime and the reproduction sandbox both use
`kagent-default`, so both move to microVMs. Upstream counts VMM and `virtiofsd`
overhead against worker capacity on top of guest RAM; the 3 GiB worker limit is
carried over from gVisor and is untested for microVMs.

Confirm the backend before trusting a run:

```sh
kubectl --context kind-triagelab get nodes \
  -o jsonpath='{.items[*].status.capacity.ate\.dev/kvm}'
kubectl --context kind-triagelab get sandboxconfig microvm
kubectl --context kind-triagelab -n kagent get workerpool kagent-default \
  -o jsonpath='{.spec.sandboxClasses[0].name}'
```

Then run the demonstration and `make test-integration` unchanged. These checks
show which pool class served the run; the report itself does not record the
isolation backend.

GitHub-hosted Linux runners expose `/dev/kvm`, so the opt-in `microvm` CI job
(the `run_microvm` input of a manually dispatched run) performs these steps on
a disposable runner. It needs an `OPENAI_API_KEY` repository secret, incurs
provider charges, and uploads the investigation report as a run artifact.

Live investigations have passed on this path in four of five runs. See
[verification.md](verification.md#microvm-backend-verification) for what ran,
an earlier unexplained failure, and what remains untested.

## Teardown

Copy investigation reports off the VM before teardown. For this dedicated
development cluster only:

```sh
kind delete cluster --name triagelab
```

Delete the ephemeral VM and attached disk through the same provisioning mechanism
that created them. Stopping a VM leaves storage charges. The investigation TTL
does not delete development cluster infrastructure, golden templates, PostgreSQL
records, object-store retention, or the VM.
