# Browser workspace

TriageLab is an independent, free product with a small Bondren signature. Its UI
uses Bondren's ink-forward drafting system: cool paper, graphite ink, a midnight
navigation frame, Plex Sans and Plex Mono, and ultramarine interaction marks.
Marcellus is reserved for the BONDREN wordmark. The workspace centers on case
inputs and recorded experiments rather than aggregate dashboard metrics.

## Run on a development VM

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

## Data flow

The browser uses an API transport module, typed domain functions, and query hooks
before presentation components. Report text renders as text, including command
output; it never becomes executable HTML. The Go service normalizes uploads with
the existing ingestion package and invokes the existing investigator. JSON stays
the canonical report. No database, queue service, browser model SDK, or alternate
execution backend is introduced.
