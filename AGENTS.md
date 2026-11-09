# Working on TriageLab

TriageLab is a Go 1.25.12 CLI and a React UI for evidence-driven investigations.
Keep it small: one investigator, one standalone sandbox per run, local
JSON/Markdown reports. There is no database, operator, custom sandbox runtime,
or agent framework. The UI is a private, single-user client of the same workflow.

## Ownership boundaries

- `internal/casefile` treats descriptions, repositories, files, and logs as untrusted data.
  It collects bounded inputs without running repository programs on the host.
- The agent package contains trusted investigator instructions and its output schema.
  KAgent executes the agent and its tools; TriageLab does not simulate its answers.
- `internal/kagent` isolates the pinned upstream MCP protocol and API shapes.
- The investigation workflow owns sandbox/session creation, input transfer,
  watchdog limits, evidence retrieval, reporting, and cleanup.
- `internal/report` serializes independently collected process evidence separately from
  model hypotheses. Keep the JSON report canonical and Markdown readable.
- `internal/ticketflow` prepares helpdesk cases. Keep policy checks and durable
  clarification reservations outside the investigator. Bind human approval to
  the exact evidence and run settings before admitting a ticket run.
- `internal/webserver` serves the UI and a bounded, loopback-only HTTP API.
  Keep credentials server-side and route new runs through the existing investigator.
- `web/` uses React 19, TypeScript, Vite, and the Bondren design language.
  Read `web/AGENTS.md` before changing the frontend.
- `deploy/` configures KAgent and Agent Substrate. KAgent owns Sandbox lifecycle;
  Substrate supplies gVisor or microVM isolation. Do not build a replacement.

## Development conventions

Use conventional Go organization for a small CLI: `cmd/` entry
points, `internal/` packages, typed configuration, standard `flag`, explicit
composition, context-aware I/O, meaningful errors, and a testable command runner
with injected writers. Prefer the standard library and minimal dependencies.
Avoid logging support data, credentials, or full imported prompts.
Pass command argument arrays; never run agent-generated commands in a host shell.
Do not read ambient Kubernetes contexts or mutate a production cluster by default.
Prefer the existing upstream security mechanisms to local approximations.

Research the pinned upstream source before changing an integration. Read
`docs/upstream.md` and `docs/development.md`; current KAgent 1.x alpha schemas differ
from older examples. SandboxTemplate is a Kubernetes CRD; a standalone Sandbox is
an API/database resource. Preserve that distinction.

Run the normal checks in the intended development environment:

```sh
make check
```

The Makefile is the canonical entry point and CI calls the same targets. Checks
include `gofmt`, `go test`, and `go vet`; use `GOFLAGS=-trimpath` for cache reuse.
Use `context.Context` for deadlines and cancellation, with a separate bounded
cleanup context. Avoid implicit global connector registration, unnecessary
compatibility shims, source-scanning guards, and abstractions without a concrete use.

Add meaningful tests for lifecycle, parsing, evidence provenance, errors, cleanup,
and security boundaries when modifying those behaviors. Unit-test fixtures are
allowed; never claim a mocked agent or sandbox proves the live integration works.
Live tests require an explicitly configured disposable KAgent/Substrate deployment
and model credentials. Describe the exact checks run and any unavailable checks.

Respect user-specified compute constraints. If the user requests remote execution,
keep builds, tests, containers, and clusters on that remote environment. Ordinary
source editing does not require new permission. Preserve unrelated work and avoid
adding approval prompts for reversible changes already within the requested task.

Maintain the short roadmap. Document known limitations plainly, especially alpha
upstream APIs, prompt injection, identity scope, output truncation, best-effort
redaction, watchdog latency, and unsuccessful cleanup.
