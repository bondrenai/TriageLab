# Working on TriageLab

TriageLab is a Go 1.27.4 CLI and a React UI for evidence-driven investigations.
Keep it small: one investigator, one standalone sandbox per run, local
JSON/Markdown reports. There is no database, operator, custom sandbox runtime,
or agent framework. The UI is a private, authenticated client (local accounts
or one OIDC provider) of the same workflow, reached over loopback.

## Ownership boundaries

- `internal/casefile` treats descriptions, repositories, files, and logs as untrusted data.
  It collects bounded inputs without running repository programs on the host.
- The agent package contains trusted investigator instructions and its output schema.
  kagent executes the agent and its tools; TriageLab does not simulate its answers.
- `internal/kagent` isolates the pinned upstream MCP protocol and API shapes.
- The investigation workflow owns sandbox/session creation, input transfer,
  watchdog limits, evidence retrieval, reporting, and cleanup.
- `internal/report` serializes independently collected process evidence separately from
  model hypotheses. Keep the JSON report canonical and Markdown readable.
- `internal/ticketflow` prepares helpdesk cases. Keep policy checks and durable
  clarification reservations outside the investigator. Bind ticket launches
  to the exact evidence and run settings. Connections persist in the private
  registry and per-connection state directories under `.tickets`; stored
  credentials never leave the server. Default to approval and let
  explicitly enabled procedures start automatically once evidence is ready.
- `internal/webserver` serves the UI and a bounded, loopback-only HTTP API.
  Keep credentials server-side and route new runs through the existing investigator.
- `internal/auth` owns workspace sign-in: local accounts with PBKDF2 hashes in
  one JSON file, in-memory sessions, and one optional OIDC provider. Roles are
  viewer (read), user (run and steer investigations), admin (define agents,
  procedures, and accounts). Keep enforcement server-side in the webserver and
  fail closed without a configured login method. The same package owns OAuth
  API clients: admin-managed clients with scopes (not roles), SHA-256-hashed
  generated secrets in one JSON file, and memory-only access tokens. OAuth
  protocol handling comes from the pinned zitadel/oidc adapter in
  `internal/auth/oauth.go`; never hand-roll grant parsing or client
  authentication.
- `web/` uses React 19, TypeScript, Vite, and the Bondren design language.
  Read `web/AGENTS.md` before changing the frontend.
- `docs-site/` builds the public documentation site (Astro/Starlight; see
  `docs-site/README.md`). The reference pages under `reference/` are generated
  by `triagelab docs` from the real flag registrations, the investigation
  config struct, and the example JSON files; a golden test under `make check`
  fails on drift. Internal links must carry the `/TriageLab` base prefix to
  pass build-time validation. Contributor-facing notes stay in `docs/`.
- `deploy/` configures kagent and Agent Substrate. kagent owns Sandbox lifecycle;
  Substrate supplies gVisor or microVM isolation. Do not build a replacement.
  The bondrenai controller fork's durable suspended-sandbox retention is
  optional: the dev installer defaults to the release controller, the fork is
  an explicit opt-in, and investigations probe the controller at run start and
  keep working without pause/resume when only a release controller is
  installed.
- `chart/` and the root `Dockerfile` package the single-user workspace for a
  dedicated cluster. Keep the loopback-only UI and port-forward access model;
  never add a Service, Ingress, or multi-replica deployment to the chart.

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
`docs/upstream.md` and `docs/development.md`; current kagent 1.x alpha schemas differ
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
Live tests require an explicitly configured disposable kagent/Substrate deployment
and model credentials. Describe the exact checks run and any unavailable checks.

Respect user-specified compute constraints. If the user requests remote execution,
keep builds, tests, containers, and clusters on that remote environment. Ordinary
source editing does not require new permission. Preserve unrelated work and avoid
adding approval prompts for reversible changes already within the requested task.

Maintain the short roadmap. Document known limitations plainly, especially alpha
upstream APIs, prompt injection, identity scope, output truncation, best-effort
redaction, watchdog latency, and unsuccessful cleanup.
