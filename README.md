# TriageLab

TriageLab is a free, Apache 2.0 tool for evidence-driven technical issue reproduction,
with a Go CLI and a browser workspace styled by Bondren.
It submits a case to a real KAgent investigator, gives it a disposable Agent
Substrate sandbox, records experiments independently, and saves JSON and Markdown
reports. It is an early, single-user proof of concept—not a production support
platform.

The included example is a small Python application with a timeout configuration
error. A useful investigation reproduces the error, identifies the units mismatch,
and verifies an explicit corrected timeout. The application itself needs no network
or dependencies. There is no simulated agent or local command-execution backend.

## Architecture

```text
Local issue + files → UI / Go CLI → KAgent MCP API
                          │          ├─ investigator Session → model provider
                          │          └─ Sandbox → Agent Substrate → gVisor
                          └─ tool events + process results → reports
```

TriageLab owns case normalization, input limits, investigation budgets, evidence
validation, cleanup, and reports. KAgent owns the agent loop, model access, tools,
Sessions, SandboxTemplates, and standalone Sandbox lifecycle. Agent Substrate owns
isolated execution, resource allocation, snapshots, identity, and egress enforcement.
There is no custom operator, agent framework, or database. The browser workspace
uses the same investigation workflow and local report files as the CLI.

The CLI creates one sandbox, uploads the bounded case files, and starts one KAgent
investigator. The investigator chooses and revises experiments through KAgent's
native MCP process/file tools. TriageLab reads durable tool activity and fetches
actual process status/stdout/stderr separately. Model claims must reference those
process IDs. Causal interpretation remains a hypothesis; the report does not
certify root causes. Both failed and successful experiments are preserved.

Go uses a conventional `cmd/` entrypoint, `internal/`
packages, explicit configuration, context cancellation, standard-library HTTP,
and Makefile checks. TriageLab has no third-party Go dependencies. The Python
example is only a reproduction fixture.

## Prerequisites

- Go **1.25.12** or later in the supported 1.25 line.
- A **dedicated Linux development VM** with Docker, kind, kubectl, Helm, and the
  **KAgent v1.0.0-rc3** CLI. The initial target is 4 vCPU / 16 GB RAM; all cluster
  work and verification should run remotely on resource-constrained laptops.
- KAgent **v1.0.0-rc3** plus its matching **Substrate v1.0.0 packaging fork**.
- A model credential configured in KAgent. The example uses `gpt-4.1`.

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
bounded to 64 files, 1 MiB per file and 4 MiB total. Symlinks, special files,
traversal, and common credential files/content are rejected. Keep submitted
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

Starting a new investigation requires the configured KAgent deployment and
`TRIAGELAB_TOKEN` in the server environment, as in the CLI quickstart. Model and
cloud usage can incur charges even though TriageLab is free. The server binds
only to loopback and accepts one active run at a time. It has no accounts or
multi-user access control; keep it behind your SSH tunnel.

The [Zendesk ticket pilot](docs/tickets.md) adds a case queue, configurable ticket
selection and clarification, and approval before the existing investigation run.

For UI iteration, run `make dev-ui` alongside the Go server. Vite proxies `/api`
to the Go server; see [UI development](docs/ui.md) for the forwarding setup.

### Checks

Run these commands on the development VM:

```bash
make fmt
make check                    # gofmt check, go vet, unit and HTTP protocol tests
make build
make check-ui                 # TypeScript check (after make install-ui)
make build-ui                 # production browser assets
TRIAGELAB_CONFIG=triagelab.example.json make test-integration
```

The opt-in integration test requires the actual deployment and model. It does
not substitute a mock if infrastructure is unavailable. Unit tests use test
HTTP servers and explicit fixture backends to exercise failure paths; they do
not establish an end-to-end demonstration. See [verification](docs/verification.md)
for what has actually been executed.

## Security and scope

Sandbox egress is denied by default. The pinned tools image has Python installed;
reproductions do not need to fetch dependencies. Operators may configure explicit
upstream EgressPolicy references or different digest-pinned templates for other
cases. MicroVMs are an upstream option requiring a suitable WorkerPool and KVM;
the initial setup uses gVisor.

KAgent's pinned development authentication is not an internet-facing security
boundary. Keep the controller private, use a dedicated cluster and identity,
and read [SECURITY.md](SECURITY.md). Imported content is always untrusted.
Prompt rules and post-execution tool auditing cannot guarantee that a model
will resist prompt injection. Hard boundaries come from upstream isolation,
egress rules, resource limits, and credentials kept outside reproduction sandboxes.

[Architecture](docs/architecture.md) · [Roadmap](TODO.md) ·
[Contributing](CONTRIBUTING.md) · [Apache 2.0 license](LICENSE)
