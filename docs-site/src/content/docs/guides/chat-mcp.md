---
title: Results chat and MCP
description: Explore saved investigation evidence with AI and connect remote MCP clients through an authenticated gateway.
---

After an investigation finishes, its report offers a saved conversation. Ask
why an experiment matters, compare hypotheses, or explore what remains unknown.
Answers link to recorded processes in their original rounds. They are model
interpretations; the canonical JSON and Markdown reports remain unchanged.

## Enable results chat

Pass a separate model configuration to the workspace:

```sh
bin/triagelab serve --chat triagelab.chat.local.json
```

Copy `triagelab.chat.example.json` from the repository. Set `endpoint` to the
complete OpenAI-compatible Chat Completions URL, `model` to a model supporting
strict JSON-schema output, and `api_key_env` to an environment variable holding
its credential. An agentgateway model route can provide this endpoint. The
example expects a separately configured gateway model named `results-chat` on
`http://127.0.0.1:3000/v1/chat/completions`; it does not install that route.

Remote model endpoints require HTTPS. Plain HTTP and an omitted API key are
allowed only for literal loopback addresses. The default timeout is 60 seconds
(maximum 120); `max_tokens` defaults to 2048 (256–8192), sent as
`max_completion_tokens`. Credentials stay server-side. The provider receives
the saved report excerpts, recent discussion, and question; provider charges
may apply.

The chat client performs one tool-free completion per question. It cannot read
host files, run commands, resume an investigator, or change evidence. This is
separate from kagent because the pinned investigator runtime always includes
file tools. New experiments still use the existing investigation workflow.

Viewers can read the conversation; users and admins can ask questions. Asking
is enabled after completion, failure, timeout, interruption, or explicit close,
and disabled during an active or paused investigation. A pending question is
saved before the model request. Answers and errors live in private `chat.json`
beside the report. A restarted workspace identifies interrupted answers without
replaying them. An uncertain submission should be refreshed and reviewed before
the question is sent again.

Questions are limited to 4096 UTF-8 bytes; answers to 16 KiB; a conversation to
64 messages. Model context contains at most 128 KiB from the newest eight
rounds, with bounded process-output excerpts, plus up to 32 KiB of recent
discussion. Omissions are marked. A citation must identify a process actually
included in that context; attribution does not prove the model's interpretation.
Only one answer runs at a time in a workspace. Saved discussion shares the
workspace's visibility and retention; it is not a private per-user conversation.

## Enable the HTTP MCP endpoint

Copy `triagelab.mcp.example.json` to a private configuration file and provide its
named credential through your secret manager. Use a randomly generated secret
of at least 32 characters. Then start the workspace with both optional features:

```sh
bin/triagelab serve \
  --chat triagelab.chat.local.json \
  --mcp triagelab.mcp.local.json
```

The endpoint is `/mcp` on the existing loopback listener, using stateless
Streamable HTTP. The official Go MCP SDK handles protocol negotiation, schemas,
and tool dispatch. No additional TriageLab listener, Service, or Ingress is
created. Browser sign-in is still required to start the workspace; MCP machine
credentials are a separate channel and cannot authenticate to the browser API.

Each configured principal has a unique `name`, a `viewer` or `user` role, and a
`token_env`. Every request must carry `Authorization: Bearer <credential>`.
Cookies and forwarded user/role headers grant no MCP access. Rotate or revoke
credentials by updating the environment/configuration and restarting the
workspace. `allowed_origins` optionally lists exact HTTPS browser origins;
without it, only absent Origin or the exact loopback origin is accepted. This
checks forwarded Origin headers; the backend does not implement cross-origin
browser CORS/preflight. If needed, configure CORS at the gateway for exact
approved client origins. Never put credentials in URLs.

| Tool | Role | Operation |
| --- | --- | --- |
| `list_investigations` | viewer | Page through saved runs |
| `get_investigation` | viewer | Read status, lifecycle review settings, model summary, and process references |
| `read_investigation_report` | viewer | Read paged JSON or Markdown with a snapshot binding |
| `get_process_evidence` | viewer | Read saved stdout/stderr for an exact round and process |
| `get_investigation_chat` | viewer | Read paged saved discussion and answer status |
| `start_investigation` | user | Start the existing workflow with a description and optional inline text files |
| `pause_investigation` | user | Request a safe pause |
| `resume_investigation` | user | Resume with additional information and the exact current lifecycle review binding |
| `close_investigation` | user | Close retained execution resources; preserve reports |
| `ask_investigation` | user | Queue an AI answer about a finished report |

Start, lifecycle, and chat tools return promptly; poll the corresponding read
tool for completion. An accepted request is not proof of success or cleanup.
Do not automatically retry an uncertain mutation. Start accepts bounded inline
UTF-8 files, never host filesystem paths or URLs to fetch. Existing agent and
sandbox admission, one active investigation, upload exclusions, lifecycle
bindings, and ticket continuation approval remain enforced. MCP does not expose
account administration, arbitrary host tools, or ticket-note delivery.

## Start investigations from a policy workflow

An external workflow or agent can decide a case is ready, select the
description and evidence, and start a standalone investigation. Use
`triagelab investigate` with a configured kagent credential or the
`start_investigation` MCP tool with a user-role credential.
[CLI options](/TriageLab/reference/cli/).

For a linked run in the built-in Ticket cases queue, enable automatic launch
in the matching procedure.
[Automatic ticket investigations](/TriageLab/guides/workspace/#automatic-ticket-investigations).

## Remote access through agentgateway

Keep the workspace private. Place agentgateway beside it, or beside an explicit
loopback SSH/port-forward tunnel, and publish the gateway through your HTTPS
edge. Expose its MCP and OAuth discovery endpoints; do not proxy the TriageLab
browser API or kagent controller to the public listener.

The repository's `deploy/agentgateway-mcp.example.yaml` targets agentgateway
**v1.7.0**. It configures strict MCP OAuth authentication, validates the issuer
and audience, restricts tool access to an explicit subject, and replaces the
external credential with `TRIAGELAB_MCP_TOKEN` for the loopback backend. Replace
the example public URL, issuer, JWKS URL, and subject; adapt the provider section
to your identity provider. Keep the admin interface private. Configure your
HTTPS edge to preserve the public origin and route the gateway's OAuth metadata
and registration paths as well as `/mcp`.

Agentgateway owns OAuth discovery and client sign-in. TriageLab only accepts the
explicit backend principal and checks its role on every tool call. The sample
maps approved callers to one shared workspace `user` identity (`mcp:gateway`),
so TriageLab records that principal rather than the individual OAuth subject.
For separate read-only access, use a distinct viewer credential and a gateway
target restricted to those callers. All principals still see the same workspace
reports. Do not use this as tenant isolation.

Use the upstream guides for [MCP authentication](https://agentgateway.dev/docs/standalone/latest/documentation/configuration/security/mcp-authn/),
[tool authorization](https://agentgateway.dev/docs/standalone/latest/documentation/configuration/security/mcp-authz/),
and [backend credentials](https://agentgateway.dev/docs/standalone/latest/documentation/configuration/security/backend-authn/key/).
The example requires your own domain, HTTPS edge, and identity-provider setup;
enabling `--mcp` does not deploy those services.

## Limits

Prompt injection remains possible in imported reports and discussion. Text is
treated as untrusted: answers render a small Markdown subset with no HTML, images
or clickable links; the chat model has no tools, but
its interpretation can still be wrong. Redaction is best effort, and context
excerpts may omit relevant material. Read the full saved evidence when a
conclusion matters. The endpoint has no per-user quota or distributed job
queue; apply admission/rate limits at the gateway for shared use. Existing
[upstream limitations](/TriageLab/reference/upstream/) and best-effort cleanup
still apply to investigations.
