# TriageLab

TriageLab is a free, Apache 2.0 tool that automates technical investigations
for tickets and issues on Kubernetes, with a Go CLI and a browser workspace
styled by Bondren. When a case arrives from Zendesk, GitHub, or Linear,
TriageLab routes it to a matching procedure, classifies what evidence is
still missing, and—after a human approves the exact evidence and run
settings—investigates it in a secure, disposable sandbox: one kagent
investigator in a gVisor or microVM Agent Substrate sandbox with egress
denied by default. Experiments are recorded independently of the model and
saved as JSON and Markdown reports; a reviewed summary can return to the
ticket with the full evidence trail. It is an early, single-user proof of
concept—not a production support platform—and all sources currently use the
Python 3.12 timeout procedure.

The included example is a small Python application with a timeout configuration
error. A useful investigation reproduces the error, identifies the units mismatch,
and verifies an explicit corrected timeout. The application itself needs no network
or dependencies. There is no simulated agent or local command-execution backend.

That example is the first of six evaluation cases in `examples/`. The others
cover a report that does not reproduce, missing information, an unrelated
nonzero exit, a deterministic failure that looks intermittent, and
prompt-injection bait beside a real bug. Expected results live in
`examples/corpus.json`, outside the uploaded case directories.
`make test-integration` scores a live run of every case, or the subset named in
`TRIAGELAB_CASES`. Only the timeout example has run against a model so far;
see [verification](docs/verification.md#evaluation-corpus-authored-not-run-live).

## Architecture

```text
Ticket/issue or local case → UI / Go CLI → kagent MCP API
                                │          ├─ investigator Session → model provider
                                │          └─ Sandbox → Agent Substrate → gVisor
                                └─ tool events + process results → reports
```

TriageLab owns ticket routing, clarification policy, human approval, case
normalization, input limits, investigation budgets, evidence
validation, cleanup, and reports. kagent owns the agent loop, model access, tools,
Sessions, SandboxTemplates, and standalone Sandbox lifecycle. Agent Substrate owns
isolated execution, resource allocation, snapshots, identity, and egress enforcement.
There is no custom operator, agent framework, or database. The browser workspace
uses the same investigation workflow and local report files as the CLI.

The CLI creates one sandbox, uploads the bounded case files, and starts one kagent
investigator. The investigator chooses and revises experiments through kagent's
native MCP process/file tools. TriageLab reads durable tool activity and fetches
actual process status/stdout/stderr separately. Model claims must reference those
process IDs. Causal interpretation remains a hypothesis; the report does not
certify root causes. Both failed and successful experiments are preserved.

Go uses a conventional `cmd/` entrypoint, `internal/`
packages, explicit configuration, context cancellation, standard-library HTTP,
and Makefile checks. The vendored OpenTelemetry SDK is the only third-party Go
dependency: it exports traces, metrics, and logs over OTLP/HTTP when the
optional `--telemetry` configuration is supplied, and stays a no-op otherwise.
See `triagelab.telemetry.example.json` and docs/upstream.md. The Python
example is only a reproduction fixture.

## Prerequisites

- Go **1.27.3** or later in the supported 1.27 line.
- A **dedicated Linux development VM** with Docker, kind, kubectl, Helm, and the
  **kagent v1.0.0-rc3** CLI. The initial target is 4 vCPU / 16 GB RAM; all cluster
  work and verification should run remotely on resource-constrained laptops.
- kagent **v1.0.0-rc3** plus its matching **Substrate v1.0.0 packaging fork**.
- A model credential configured in kagent. The example uses `gpt-4.1`.

See [development setup](docs/development.md) for exact installation, private
port-forwarding, credential setup, and teardown. The pinned interfaces are
prerelease APIs; see [upstream contracts](docs/upstream.md) before upgrading.
Do not install this development configuration into a production cluster.

## Quickstart

After installing the development cluster and applying the generated resources:

```bash
make build
# In another terminal on the VM:
kubectl -n kagent port-forward svc/kagent-controller 8083:8083
# In your CLI shell (do not echo this token):
export TRIAGELAB_TOKEN="$(kubectl -n kagent create token triagelab \
  --audience=kagent-invocation --duration=1h)"
./bin/triagelab doctor --config triagelab.example.json
./bin/triagelab investigate --config triagelab.example.json \
  --description-file examples/config-bug/issue.md --repo examples/config-bug
```

For your own case:

```bash
./bin/triagelab investigate --description 'The service rejects valid requests' \
  --repo ./small-reproduction --log ./service.log --file ./config-example.json
```

Files are copied into `/data/workspace`, never mounted from the host. Inputs are
bounded to 64 files, 1 MiB per file and 4 MiB total. Symlinks inside inputs
(including a named repository or file that is itself a link), special files,
traversal, and common credential files/content are rejected. Links in the path
leading to an input, such as macOS `/tmp`, are followed. Keep submitted
repositories small and sanitized. Obvious-secret detection is not a substitute
for reviewing support data before sending it to a model provider.

Each run writes private `reports/<case-id>/report.json` and `report.md`, including
partial results on failures. Ctrl-C and SIGTERM trigger cancellation and cleanup.
A server-side sandbox TTL is the fallback for client crashes or connectivity loss.
If cleanup fails, the report retains IDs and the CLI exits nonzero:

```bash
./bin/triagelab cleanup --config triagelab.example.json \
  --report reports/<case-id>/report.json
```

Exit codes: `0` reproduced; `1` not reproduced; `2` invalid input;
`3` inconclusive; `4` infrastructure/agent/protocol failure; `5` cleanup incomplete;
`124` time limit; `130` interrupted. A nonzero exit never means the report should
be discarded. Infrastructure failures are not reported as “not reproduced.”

## Development and verification

### Browser workspace

Build and run on your development VM (Node.js 24 is required for the UI):

```sh
make install-ui
make build-ui
make build
./bin/triagelab serve --config triagelab.example.json
```

Open `http://127.0.0.1:8080` through an SSH local port forward. The UI can submit a
description and files, follow a run, cancel it, inspect process evidence, and
download JSON or Markdown. Existing reports in `reports/` appear in its history.
The separately labelled example shows the included, previously recorded live
investigation; it does not start a model or sandbox.

Starting a new investigation requires the configured kagent deployment and
`TRIAGELAB_TOKEN` in the server environment, as in the CLI quickstart. Model and
cloud usage can incur charges even though TriageLab is free. The server binds
only to loopback and accepts one active run at a time. It has no accounts or
multi-user access control; keep it behind your SSH tunnel.

The **Agents** workspace and `triagelab agent-manifest` render native kagent
definitions for declarative investigators or compatible custom runtime images.
Choose a provider and model, attach specific MCP tools, and select the deployed
agent for a manual run. See [defining agents](docs/agents.md).

The [Zendesk ticket pilot](docs/tickets.md) adds a case queue, configurable ticket
selection and clarification, and approval before the existing investigation run.
[GitHub issue support](docs/github-issues.md) brings issue and attachment imports,
reviewed clarifications with bounded follow-ups, and reviewed investigation
comments to one configured repository through the same workflow. GitHub comments
are visible to everyone with repository access and public in public repositories.
[Linear issue support](docs/linear-issues.md) brings the same workflow to one
configured team through a server-side API key. Linear comments are visible to
everyone with workspace access. All sources currently use the Python 3.12
timeout procedure; select the source with the configuration passed to
`serve --tickets`.

For UI iteration, run `make dev-ui` alongside the Go server. Vite proxies `/api`
to the Go server; see [UI development](docs/ui.md) for the forwarding setup.

### Helm deployment

The same single-user workspace can run as one pod next to an existing
kagent/Substrate installation, installed from the public chart
`oci://ghcr.io/bondrenai/charts/triagelab`. The UI still binds to loopback
inside the pod and is reached through `kubectl port-forward`; the chart
creates no Service or Ingress by design. See [Helm deployment](docs/helm.md).

### Documentation site

The public documentation lives in `docs-site/` (Astro/Starlight) and publishes
to GitHub Pages at <https://bondrenai.github.io/TriageLab/> on pushes to main.
Its CLI and configuration reference pages are generated from the actual flag
registrations and config structs by `triagelab docs`; a golden test under
`make check` fails if they drift, and the build validates internal links.
See `docs-site/README.md` for the workflow (`make dev-docs`, `build-docs`,
`docs-generate`). Contributor-facing notes stay in `docs/`.

### Checks

Run these commands on the development VM:

```bash
make fmt
make check                    # gofmt check, go vet, unit and HTTP protocol tests
make race                     # the same tests under the race detector (several minutes)
make lint                     # staticcheck, pinned via go run (see staticcheck.conf)
make vulncheck                # govulncheck, pinned via go run
make build                    # VERSION defaults to git describe, else "dev"
make dist                     # static linux/darwin amd64/arm64 binaries + SHA256SUMS in dist/
make check-ui                 # TypeScript check (after make install-ui)
make build-ui                 # production browser assets
TRIAGELAB_CONFIG=triagelab.example.json make test-integration
```

CI runs `make check` on Ubuntu and macOS, and `make lint` and `make vulncheck` on Ubuntu. A `v*` tag
release repeats those checks, then publishes the multi-arch image, the chart,
and static CLI binaries with checksums (the binaries do not include the web UI
that `serve` needs); see [Helm deployment](docs/helm.md#publishing-maintainers).

The opt-in integration test requires the actual deployment and model. It does
not substitute a mock if infrastructure is unavailable. Unit tests use test
HTTP servers and explicit fixture backends to exercise failure paths; they do
not establish an end-to-end demonstration. See [verification](docs/verification.md)
for what has actually been executed.

## Security and scope

Sandbox egress is denied by default. The pinned tools image has Python installed;
reproductions do not need to fetch dependencies. Operators may configure explicit
upstream EgressPolicy references or different digest-pinned templates for other
cases — through reviewed manifests or the workspace's Sandboxes and Egress editors,
with per-run template selection gated behind `--allow-resource-writes`. MicroVMs
are an upstream option requiring a suitable WorkerPool and KVM;
the initial setup uses gVisor.

kagent's pinned development authentication is not an internet-facing security
boundary. Keep the controller private, use a dedicated cluster and identity,
and read [SECURITY.md](SECURITY.md). Imported content is always untrusted.
Prompt rules and post-execution tool auditing cannot guarantee that a model
will resist prompt injection. Hard boundaries come from upstream isolation,
egress rules, resource limits, and credentials kept outside reproduction sandboxes.

[Documentation](https://bondrenai.github.io/TriageLab/) · [Architecture](docs/architecture.md) · [Roadmap](TODO.md) ·
[Contributing](CONTRIBUTING.md) · [Apache 2.0 license](LICENSE)
