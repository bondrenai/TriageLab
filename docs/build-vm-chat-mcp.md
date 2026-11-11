# Build VM: results chat and MCP

The private build-VM deployment uses the existing `gpt-4.1` model credential from
`Secret/kagent-openai` in namespace `kagent`, reached with the explicit
`triagelab-v1-live/kubeconfig` and `kind-triagelab` context. The model key is fetched
into the agentgateway process environment at startup. It is not copied into a
configuration file or into the TriageLab process.

The installed release is under
`/home/will_bondren_ai/triagelab-v1-live/releases/cel-monaco-20261111` on
`bondren-build`. Its private runtime configuration, secret files, launch script,
and verification records are under
`/home/will_bondren_ai/triagelab-v1-live/chat-mcp`. Existing reports and accounts
remain in `triagelab-v1-live/ui-reports`; the existing admin account is preserved.
Agentgateway remains on `releases/chat-mcp-20261111`. The previous workspace
release is retained there for rollback.

The workspace now enables the existing Jira test-site connector using private
`chat-mcp/tickets.json` and the VM's `.triagelab-jira-token`. Open **Ticket cases →
Edit procedures → Raw CEL** to use Monaco autocomplete and server validation.
The `triagelab_python_timeout` procedure now automatically starts investigations
for Jira tickets with that label once its existing evidence requirements pass.
Its match and launch conditions are CEL policies saved through the UI. No ticket
is tracked just by opening the editor or previewing a draft. Clarification sends and internal notes are disabled in this
connector configuration.


## Access

From the workstation:

```sh
ssh -N \
  -L 8096:127.0.0.1:8096 \
  -L 3001:127.0.0.1:3001 \
  bondren-build
```

Open `http://127.0.0.1:8096` and use the existing workspace account. Configure an
HTTP MCP client with `http://127.0.0.1:3001/mcp` and a Bearer credential from the
VM's private file:

```text
/home/will_bondren_ai/triagelab-v1-live/chat-mcp/secrets/mcp-client.key
```

For a client supporting credentials through environment variables, capture the
key without printing it:

```sh
export TRIAGELAB_MCP_CLIENT_KEY="$(ssh bondren-build \
  cat /home/will_bondren_ai/triagelab-v1-live/chat-mcp/secrets/mcp-client.key)"
```

Supply that variable using the client's bearer-token setting. Do not put it into
a URL, repository configuration, or chat message. The gateway replaces this
credential with a distinct backend principal (`mcp:gateway`, role `user`). It
does not authenticate a browser UI session. A separate backend viewer credential
is available in `secrets/mcp-viewer.key` for read-only access through an explicit
workspace-port tunnel.

This deployment uses machine API keys over SSH. It does not publish a public
endpoint or implement interactive OAuth login. The optional public OAuth recipe
is described in the [chat and MCP guide](../docs-site/src/content/docs/guides/chat-mcp.md).

## Services

These user systemd services are enabled, with user lingering enabled so they
survive SSH disconnection and restart on VM boot:

| Service | Loopback endpoint | Purpose |
| --- | --- | --- |
| `triagelab-workspace` | `127.0.0.1:8096` | Authenticated UI, API, and backend MCP |
| `triagelab-agentgateway` | `127.0.0.1:3000` | Authenticated `results-chat` model route to `gpt-4.1` |
| `triagelab-agentgateway` | `127.0.0.1:3001/mcp` | Authenticated MCP frontend |
| `triagelab-kagent-forward` | `127.0.0.1:8083` | Existing disposable controller connection |

Inspect them without displaying credentials:

```sh
ssh bondren-build systemctl --user status \
  triagelab-workspace triagelab-agentgateway triagelab-kagent-forward
```

Agentgateway is pinned to **v1.7.0**. Its auxiliary admin, metrics, and readiness
listeners are disabled. Request access logging is disabled, and runtime logging
is warning level. Incoming model, incoming MCP, and backend MCP credentials are
distinct; generated key files are private. Model calls are limited to the
`results-chat` alias. No provider key is exposed to the browser or MCP client.

The workspace launcher mints a fresh **24-hour** kagent invocation credential
when it starts. Chat's model credential is independent. When investigation
access expires, restart the workspace while no investigation or answer is active:

```sh
ssh bondren-build systemctl --user restart triagelab-workspace
```

A restart clears browser sign-in sessions; sign in again. It does not delete
reports or saved chat. After rotating the model Secret, restart
`triagelab-agentgateway` to load the new credential. Local key rotations require
restarting the services that read those keys. The deployment does not schedule
automatic workspace restarts, which could interrupt a running investigation.

## Release and recovery

The private runtime `release.json` records the binary hash and release paths.
`users-before-upgrade.json` is a private pre-upgrade account backup.
The Monaco rollout's previous launcher, release metadata, account backup, file
hashes, and browser evidence are in
`verification/cel-monaco-deployment-20261111`. `release.json` identifies the
workspace and gateway release directories separately. The previous
binary and UI remain at `/work/TriageLab-kagent-v1`; they were not overwritten.
Do not run old and new workspace processes against the same account store at the
same time. If rollback is needed, stop the new workspace service first, review
the existing report/account state, and launch the earlier binary with a fresh
kagent credential and its original arguments.

Live test details and the distinction between real-model and fixture checks are
recorded in [verification](verification.md).
