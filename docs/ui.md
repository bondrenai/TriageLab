# Browser workspace

TriageLab is an independent, free product with a small Bondren signature. Its UI
uses Bondren's ink-forward drafting system: cool paper, graphite ink, a midnight
navigation frame, Plex Sans and Plex Mono, and ultramarine interaction marks.
Marcellus is reserved for the BONDREN wordmark. The workspace centers on case
inputs and recorded experiments rather than aggregate dashboard metrics.

The TriageLab mark is a flask inside drafting corner marks, with one
ultramarine evidence dot. `web/src/components/brand-mark.tsx` draws it in
currentColor for the UI. Static copies for the docs header and README are in
`docs-site/src/assets/`. The favicon in `web/public/` and `docs-site/public/`
drops the corner marks so it stays legible at 16px. When you change the
shape, update all of these files.

## Run on a development VM

For the configured `bondren-build` workspace with results chat and agentgateway,
see [build VM access and operations](build-vm-chat-mcp.md).

Use Node.js 24 and Go 1.27.3. Install dependencies and build on the VM:

```sh
make install-ui
make build-ui
make build
bin/triagelab serve --config triagelab.example.json --output reports
```

The default listener is `127.0.0.1:8080`; `--assets` defaults to `web/dist`.
Run from the repository root or pass an absolute assets directory. Forward the
port with your existing SSH configuration, for example:

```sh
ssh -N -L 8080:127.0.0.1:8080 YOUR_DEV_VM
```

Open `http://127.0.0.1:8080`. The UI lists existing CLI reports in the selected
output directory. Its saved example is bundled from the repository's real
verification report and is explicitly separate from your investigation history.
Viewing history or the example does not require a running model provider.

To investigate, configure kagent and its model credential as described in
[development setup](development.md), forward the controller on the VM, and set
`TRIAGELAB_TOKEN` in the server's environment before starting it. Neither that
token nor the model key belongs in browser configuration. Free source code does
not include provider or VM usage; those services may charge for a run.

## Iterate

Keep the Go server running, then run `make dev-ui` on the VM. Vite listens on
loopback port 5173 and proxies `/api` to the Go server. Forward that port too:

```sh
ssh -N -L 5173:127.0.0.1:5173 YOUR_DEV_VM
```

Open `http://127.0.0.1:5173` for hot reload. Ordinary UI iteration should stay
focused on the affected screen. Before landing a change, run the checks relevant
to its behavior on the VM:

```sh
make check-ui
make build-ui
make check
```

The API is a local interface for a small team, not a hosted service. It accepts one
investigation at a time. Closing a tab leaves the investigation running; use
Cancel to stop it. Server shutdown also cancels active work and allows bounded
cleanup. After an abrupt crash, inspect the report and use the CLI cleanup
command if resources remain. A recorded outcome does not override cleanup errors.

## Reading and continuing an investigation

The investigation brief brings together the current agent analysis, reported
observations, blocking questions, and next actions. Observations and experiment
assessments remain model interpretations. The experiment register contains the
commands and outputs collected independently from the sandbox.

Open an evidence reference to review the claim beside its recorded command,
exit status, output, and collection warnings. Reproduction references, hypothesis
references, and contradicting references all point to the selected round's saved
process evidence. Missing or incomplete output never becomes evidence of success.
Older reports can be read without the newer experiment assessments.

Execution state, reproduction outcome, and resource state are shown separately.
A paused investigation can already have reproduced a symptom, while its sandbox
and session remain retained. Answer blocking questions through Resume, review
the new information and limits, and approve the continuation. Closing releases
execution resources and keeps saved reports.

Round history records added information, experiments, analysis, and outstanding
questions without rewriting earlier rounds. When viewing a historical round,
lifecycle controls still operate on the latest investigation. Downloads include
all saved rounds; they do not include the private operational lifecycle record.

The queue separates active work, investigations needing attention, and recent
reports. A transient refresh failure keeps the last confirmed rows visible with
an explanation; an access denial hides the protected rows. Ticket handoff keeps
the destination, audience, reviewed text, and confirmed or uncertain delivery
visible. Reviewing a note does not confirm its conclusions.

## Sign-in and roles

The UI requires sign-in. Sessions are memory-only cookies, so everyone signs
in again after a restart. Two methods can be configured, and they may coexist:

- **Local accounts** live in `<output>/.auth/users.json` beside the reports,
  hashed with PBKDF2-SHA256. Bootstrap and manage them from the CLI:

  ```sh
  echo 'initial-password' | bin/triagelab user add --username admin --role admin --password-stdin
  bin/triagelab user list
  ```

  Admins can also manage accounts in the UI (Accounts page). Manage accounts
  from the CLI while the server is stopped; a running server holds its own
  copy and would overwrite CLI changes on its next save.

- **Single sign-on** uses one OpenID Connect provider configured through an
  optional file passed to `serve --auth auth.json` (see `auth.example.json`).
  The client secret is read from an environment variable, never the file.
  Roles come from a direct role claim or by mapping a groups claim through
  `role_map`; accounts that match no mapping receive `default_role`
  (least-privilege `viewer` unless configured otherwise).

Provider notes from end-to-end verification against Keycloak 26 and
Zitadel v4.20 (2026-11-09):

- **Keycloak** (confidential client, `client_secret_post` + PKCE): map
  workspace roles from a `groups` claim via a client scope with the group
  membership mapper — in Keycloak 26 the mapper provider id is
  `oidc-group-membership-mapper`, and the scope must be attached to the
  client's default scopes. Realm roles only reach the access token by
  default, so a dedicated groups mapper is the reliable path.
- **Zitadel** (public client, PKCE only): request the scope
  `urn:zitadel:iam:org:projects:roles` (plural "projects"); the claim in the
  ID token is `urn:zitadel:iam:org:project:roles` (singular) and is an
  object keyed by role name, which the workspace accepts. Roles reach the ID
  token only when the app sets the ID-token role/userinfo assertions and the
  project sets `project_role_assertion`. Human users need an initial
  password set through the API (`initial_password` at creation, or the
  password endpoints afterwards).

Three roles are enforced server-side on every API route:

| Role | Can | Cannot |
| --- | --- | --- |
| viewer | Read reports, cases, deployment status | Change anything |
| user | Everything a viewer can, plus launching and steering investigations and ticket actions | Define agents, sandbox templates, egress policies, procedures, or accounts |
| admin | Everything, including saving kagent definitions, ticket procedures, and account management | |

Each report records `started_by`. Sign-in attempts are rate-limited per
account. There is no per-user action audit log yet.

### API clients

Roles are for people; programs use OAuth API clients. Admins create them on
the **API clients** page (or `PUT`/`POST` under `/api/auth/admin/clients`),
choosing a client ID and the `investigations:run` and/or
`investigations:read` scopes. The generated secret is shown once. Clients
persist in `<output>/.auth/clients.json` (`serve --clients` to relocate) as
SHA-256 hashes; access tokens are memory-only. The application exchanges the
secret at `POST /oauth/token` (RFC 6749 client credentials, zitadel/oidc
protocol handling) and sends `Authorization: Bearer …` on the run API. A
`run`-scoped client sees and steers only the runs it started
(`started_by: client:<id>`); `read` is a workspace-wide read grant. Bearer
requests bypass the `X-TriageLab-Request` marker rule — the token is a
credential in the request itself — but stay inside the loopback and
same-origin checks and never reach account, ticket, or definition routes.
The public guide is [API clients (OAuth)](https://bondrenai.github.io/TriageLab/guides/api-clients/).

## Data flow

Results chat is configured separately with `serve --chat`; it uses a tool-free
completion endpoint to discuss saved evidence after a run finishes. Questions,
answers, and interrupted/error state are stored in private `chat.json`, never
in the canonical report. Viewers can read; users and admins can ask. The report
page preserves literal text, saved questions after failures, and citations to
the exact evidence round. Questions are not automatically retried.

`serve --mcp` adds a bearer-authenticated Streamable HTTP endpoint on the same
loopback listener. Its machine credentials do not grant browser sessions. An
optional agentgateway OAuth edge supplies a configured viewer/user principal;
TriageLab still authorizes every tool and reuses the existing investigation and
ticket-policy admission paths. Setup, bounds, and deployment instructions are in
the [chat and MCP guide](../docs-site/src/content/docs/guides/chat-mcp.md).

The browser uses an API transport module, typed domain functions, and query hooks
before presentation components. Report text renders as text, including command
output; it never becomes executable HTML. The Go service normalizes uploads with
the existing ingestion package and invokes the existing investigator. JSON stays
the canonical report. No database, queue service, browser model SDK, or alternate
execution backend is introduced.

## CEL policy editor

Raw policy fields lazy-load a local adaptation of Agentgateway v1.7.0's Monaco
editor. The condition builder continues to work without loading Monaco.
`web/src/lib/cel-monaco.ts` provides language support; `ticket-cel-schema.ts`
mirrors the normalized ticket attributes in `internal/ticketflow/cel.go`.
Each model carries its source so multiple editors do not share connector state.

The existing admin-only validator supplies debounced compiler diagnostics.
Obsolete requests are aborted, stale markers are cleared, and CEL code-point
locations are converted to Monaco's UTF-16 columns. Save-time checks remain
server-side. Compilation cannot prove that a dynamic map key exists on a ticket;
use ticket preview to check actual values and missing custom fields.

Monaco 0.57.0 and the React wrapper 4.7.0 are pinned. Monaco's DOMPurify
dependency is overridden to patched 3.4.17. Only editor features are bundled,
without gateway extensions or unrelated language packs. The editor and worker
are served from the workspace, with no CDN requests. Monaco needs runtime CSS,
so CSP permits inline styles while scripts and workers remain restricted to
`'self'` without `unsafe-eval`. Attribution and licenses ship with the UI under
`/licenses/`; see [third-party notices](../web/THIRD_PARTY_NOTICES.md).
