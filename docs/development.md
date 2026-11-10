# Development and real integration setup

Use a dedicated disposable Linux VM for Kubernetes, image pulls, builds, and test
runs. Editing and reading source can stay on a laptop. An initial allocation of
4 vCPUs and 16 GiB RAM is reasonable for this small development stack; increase
only if measurement shows pressure. Set a short automatic shutdown deadline,
save reports, then delete the VM and its disk when finished. TriageLab itself
does not provision or bill cloud resources.

The setup below is a **private, single-user development environment**. Do not
install it in a production cluster or publish its controller port. The released
KAgent controller defaults to a no-op application authorizer. Even with the
service-account verifier enabled, the development authentication mode still has
other identity paths. An authenticated gateway and a real application authorizer
are prerequisites for shared or exposed use. See [SECURITY.md](../SECURITY.md).

To run the same single-user workspace as one pod inside this dedicated cluster
instead of on the VM, use the [public Helm chart](helm.md); its access model is
unchanged (loopback listener, `kubectl port-forward` only).

## Prerequisites on the VM

- Linux with Docker or a compatible kind provider. Docker is used to host kind;
  its socket is never mounted into an investigator or reproduction sandbox.
- Go 1.27.3, `kubectl`, Helm 3, and kind 0.33.0. The included Python fixture
  runs inside the reproduction sandbox, not on the development host.
- A C compiler (`gcc` or `clang`) for `make race`, which runs the tests with
  the race detector and needs cgo; the released binary is built without it.
- KAgent CLI **1.0.0-rc3**, matching the charts and runtime images.
- A provider account/key for the real agent. The example uses the upstream
  `gpt-4.1` model, with bounded investigation limits. Model calls incur provider charges.
- Enough free disk for Kubernetes and Substrate images/snapshots (30 GiB free
  is a useful starting allowance).

Install the released KAgent binary rather than compiling the large upstream
project. For Linux amd64:

```sh
mkdir -p .tools
curl -fL https://github.com/kagent-dev/kagent/releases/download/v1.0.0-rc3/kagent-linux-amd64 \
  -o .tools/kagent
echo '8d0ba56fe8382f20511ce585810cf1ac043b361482d15ac7163264699b882e63  .tools/kagent' \
  | sha256sum -c -
chmod +x .tools/kagent
export PATH="$PWD/.tools:$PATH"
```

For other architectures, use the matching release asset and verify its published
checksum. Never substitute a different KAgent version without rechecking the
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

This uses KAgent's installer to install the pinned Substrate Helm distribution,
its pod certificate controller, Atelet, the development PostgreSQL database,
RustFS snapshot store, and KAgent. It also configures Substrate's ingress
identity, controller access grants, and required preview gates. TriageLab adds
only ordinary KAgent resources, rendered by `triagelab manifest`. The script
refuses any Kubernetes context except `kind-triagelab`.

The rendered `triagelab-tools` ClusterIP Service sends investigator MCP calls to
the same controller through a separate hostname. Keep that hostname distinct
from KAgent's control-plane URL: rc3 replaces credentials at the control-plane
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
KAgent endpoint to `0.0.0.0`. The CLI sends the selected case and files to KAgent,
so use only material you are authorized to disclose to that model provider.

The investigator should inspect the fixture, run the failing application, and
perform a verification experiment with corrected configuration. Success requires
real process records, failure output, and evidence references in the final JSON
and Markdown. Model output alone is not proof of execution.

## Check and troubleshoot

Run tests, lint, and formatting on the VM:

```sh
make check
make race
make lint
make vulncheck
```

`make race` repeats the tests under the race detector. It takes several
minutes because the sign-in tests hash passwords.

`make lint` and `make vulncheck` download pinned staticcheck and govulncheck
builds with `go run`, so they need network access to the Go module proxy.

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

KAgent's sandbox TTL expires even if the CLI disappears. Normal and interrupted
runs also explicitly delete their sandbox. A process-kill acknowledgment is not
proof it exited; the CLI observes completion and records cleanup failures. If a
controller outage prevents cleanup, retain the report's IDs and retry against
the same controller once it recovers.

For the optional microVM backend, first provision a Linux node with usable
`/dev/kvm`, install Substrate's microVM runtime assets, then select
`substrateWorkerPool.sandboxClass: microvm`, `sandboxConfigRef: microvm`, and the
matching `ateworker-microvm:v1.0.0` image. A template references the selected pool;
TriageLab needs no new runtime implementation. This path needs separate real
integration testing and is not implied to work merely because gVisor works.

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
KAgent RPC spans, HTTP server spans, and the run/cleanup metrics. For a
collector that requires authentication, put the header lines in the
environment variable named by `headers_env` (never in the file). The unit
tests assert the exported OTLP payloads without a live collector; live export
is exercised only to the extent recorded in docs/verification.md.

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
