# Security model and limitations

TriageLab runs commands selected by a model and ingests untrusted technical reports.
Use a dedicated, disposable deployment and identity. This proof of concept is not
a multi-tenant service or a production incident-remediation tool.

## Execution boundary

The CLI never executes imported application code or agent-generated commands on
its host. It uploads selected files into a standalone kagent Sandbox. Agent Substrate
provides the gVisor boundary; a configured microVM worker pool is an optional stronger
isolation backend. The demonstration requires no host Docker socket, filesystem
mount, privileged interface, or production credential inside that sandbox.

The supplied SandboxTemplate has no egress policy references. kagent/Substrate deny
outbound connections by default, including package downloads. Preinstall dependencies
in a reviewed, digest-pinned tools image, or explicitly review and grant narrowly
scoped egress through upstream policies; the workspace's Egress and Sandboxes editors
author those same upstream resources behind `--allow-resource-writes`, and every
grant stays an explicit operator action. Do not enable broad egress just to make an
experiment pass. Images and the trusted Substrate host components remain part of
the supply-chain and isolation trust boundary.

CPU, memory, and maximum sandbox lifetime are configured on the upstream installation.
The CLI also limits investigation duration, observed experiment count, process runtime,
input sizes, and collected output. Client watchdogs act after observing upstream state;
they are not precise server-side command or tool-call quotas. A process may exceed its
client limit by polling and request latency. Use upstream resource limits and TTL as
the backstop if the CLI crashes, loses connectivity, or is killed without cleanup.

## Identity and credentials

The example installation deliberately uses kagent's development authentication mode.
Keep it private behind loopback port forwarding or an SSH tunnel. A development
`user_id` is an ownership partition, not an authentication credential; never expose
this endpoint to an untrusted network. For shared deployments, configure upstream
authenticated identity and authorization first. TLS protects transport; it does not
turn the development identity header into authentication.

The investigator's remote MCP allowlist contains only the configured sandbox process
and file tools. This allowlist does not remove every Go ADK built-in tool: built-in
read/write/edit tools remain available inside the investigator's own runtime.
The supplied configuration disables Bash and file-search tools; disabling file
search removes list/grep, not all local file access. Do not place sensitive files
in that investigator runtime or treat the MCP allowlist as its complete capability
inventory. Provider credentials belong to kagent's model/egress configuration, not uploaded files or
SandboxTemplate environment variables. TriageLab reads its API token from a named
environment variable and does not place it in the reproduction environment. The
trusted sandbox MCP binding forwards the verified invocation token to preserve
ownership. Keep global runtime token propagation disabled before attaching MCP
servers. In rc3 it forwards the caller credential and identity to all MCP
servers. Custom runtime images can receive the invocation credential and must be
trusted. Review model and server references for explicit caller passthrough.
See [custom agent deployment](docs/agents.md#deploy-and-use).

Ownership is broader than a single investigation: an authenticated identity can
access other sandboxes it owns. TriageLab's instructions constrain the agent to the
supplied sandbox ID, but that prompt is not an API authorization boundary. Use a
dedicated identity with no unrelated sandboxes. The current MVP does not mint a
capability restricted to one sandbox or enforce a per-run tool allowlist server-side.

## Untrusted content and evidence

Trusted agent instructions explicitly label case text, repositories, filenames,
logs, and tool output as untrusted data. This reduces prompt-injection risk but
cannot guarantee prevention. Isolation, egress denial, narrow tools, ownership,
resource limits, and cleanup are the effective boundaries. Do not add production
tools, ticket-writing tools, or privileged operations to the investigator. External
mutations or privileged access require explicit authorization and a separate design.

The CLI retrieves process state and output independently from kagent. This confirms
the recorded execution results came from the service, not that arbitrary output is
true or that a model's causal explanation is correct. Reports distinguish observed
experiments from hypotheses, retain failures, and do not assert confirmed root cause.

Local ingestion refuses symlinks inside the submitted material (including a named
repository or file that is itself a link), special files, obvious credential filenames
and patterns, unsafe destination names, and oversized inputs. Links in the path that
leads to a named input are followed when it is opened, so name inputs through
directories you control, not through an extracted archive. It excludes version-control
and common generated/vendor directories. These rules are intentionally conservative,
and secret detection is incomplete. Review selected files before submitting a case.
Support text and relevant file/output contents may be sent to the configured model
provider; choose a provider and retention configuration appropriate for that data.

Ticket prose crosses two sanitizer boundaries around the investigator. The input
sanitizer delouses requester fact text before it becomes case evidence, so the
description, stored facts, and sandbox environment carry the same redacted text
(credential assignments, known token shapes, terminal escapes, and control
characters are rewritten), and the applied redaction classes are recorded in the
case source for provenance. A fact that contains a configured credential holds
the whole case for review instead: a customer echoing one of our credentials is
an incident to inspect, not text to launder. Attachments and the composed
description keep the conservative refusal checks above and are never silently
rewritten, because code evidence must run as submitted. The output sanitizer
never rewrites: an outbound clarification or investigation comment that still
contains credential-like content, the identity of another tracked requester, or
verbatim internal note text is held for operator review with its findings shown
next to the blocked body. Both sanitizers are pattern-based best effort — they
can miss variants and can hold harmless drafts (short or common names match
loosely) — and do not replace operator review of the exact outbound text.

Reports use private file/directory permissions, atomic replacement of each file,
best-effort secret redaction, and fenced rendering of untrusted Markdown. They can
still contain sensitive support data. Redaction is not a guarantee, and rendered
reports should be reviewed before sharing. Upstream logs, model-provider retention,
snapshots, and backups have their own retention policies; deleting a session or
sandbox does not prove all historical data has been erased.

## Browser workspace

The browser workspace is a local interface that requires sign-in. Its server
refuses non-loopback listeners, validates request hosts and origins, and
requires a custom request header for mutations. Sessions are memory-only
cookies; the server enforces three roles (viewer, user, admin) on every API
route and fails closed when no login method is configured. Local passwords
are stored as PBKDF2-SHA256 hashes in a file beside the reports; the optional
OpenID Connect flow verifies ID-token signatures against the provider's JWKS
with PKCE, one-time state, and nonce checks. Sign-in attempts are rate-limited
per account, and each report records who started it.

These are workspace roles, not isolation between accounts: everyone signed in
reads the same shared report volume. Anyone who can reach the local port can
attempt sign-in, so keep the port forward private; use an SSH tunnel for a
remote VM, never a public proxy. The browser receives redacted reports, not
the configured API token. Selected uploads use the same ingestion limits and
credential screening as CLI inputs. There is no per-user action audit log
yet, and a running server does not pick up CLI account changes (manage
accounts in the UI or restart).

The optional [Helm chart](docs/helm.md) runs the same workspace as one pod in
a dedicated cluster. The loopback rule is unchanged: the UI is reached through
`kubectl port-forward`, and the chart creates no Service or Ingress. Its
`allow_cluster_http` setting additionally permits plain HTTP only to
`*.svc`/`*.svc.cluster.local` kagent endpoints inside that cluster's pod
network; every other host still requires HTTPS or loopback.

With the optional [Zendesk procedure](docs/tickets.md), the server can add
public clarifications to explicitly tracked tickets after the operator approves
the first draft and requester. Public replies use fixed templates, and every
outbound clarification passes the output sanitizer before it can be sent.
Optional internal notes contain a reviewed, bounded summary and selected
evidence from a completed linked run. The ticket record journals comment attempts before
sending and holds uncertain writes for manual inspection. OAuth uses PKCE and
a one-time loopback callback bound to the initiating browser. Credentials stay
on the server and are sent only to the configured Zendesk account. Temporary
attachment URLs on numbered Zendesk attachment hosts are fetched anonymously.
Public clarification or internal notes require `tickets:write`. Keep the
case state private and persistent across restarts. Use one workspace per
account for automatic replies and test on a disposable ticket first.

The GitHub issue connector is scoped to one configured repository. A server
environment token is sent only to `api.github.com`; API redirects are refused.
Posting requires explicitly enabled clarification or investigation comments
and a token with Issues write permission. The operator reviews the first
clarification and author before approving bounded follow-ups. Investigation
summaries require separate review of the exact body and repository audience.
All GitHub comments are visible to everyone with repository access and public
in public repositories; GitHub has no private internal notes. Summaries render
imported content in literal Markdown code blocks to avoid accidental mentions,
links, or images, and pass the same output sanitizer before posting; a summary
that trips it is shown as blocked with its findings instead. Confirmed outgoing
clarification IDs are excluded from author
replies even when the token account is also the issue author.

Delivery reservations and receipts persist in the private case state. Uncertain
sends are never automatically retried, including after restart. A freshness read
precedes each POST, but GitHub lacks an atomic conditional comment write, so a
concurrent edit can still race the send. Public text uploads use a separate
unauthenticated download path with a bounded body and one narrowly allowed
GitHub attachment redirect. Private attachments and arbitrary linked downloads
are unsupported. Facts and reproduction files come only from the issue author.
Issue and comment contents, attachment references and bytes, policy, and run
settings bind the investigation approval and are checked again before launch.
Imported data remains untrusted, and approval does not establish that the
reproduction is safe. See the [GitHub setup and limits](docs/github-issues.md).

## Telemetry export

Optional OpenTelemetry export (`--telemetry`, chart `telemetry:` values) sends
traces, metrics, and logs to a configured OTLP/HTTP collector. It is disabled
by default; nothing leaves the process without it. The export is a new
outbound path from the workspace: point it at a collector you control, prefer
HTTPS off-loopback (plain HTTP is loopback-only or an explicit `insecure`
opt-in for an in-cluster Service), and supply authentication headers through a
Secret-backed environment variable, never values files.

Instrumentation is restricted to fixed vocabularies and identifiers: run IDs,
statuses, outcomes, launch triggers, MCP method and tool names, upstream error
kinds, collapsed route patterns, and durations. Case descriptions, attachments,
model prompts, tool outputs, credentials, and account names are never
exported; phase log messages pass through the report redactor first. Export is
best effort: collector failures are reported on stderr and never alter an
investigation or its report.

## Cleanup and failure handling

The workflow attempts to delete the session and sandbox after success, failure,
timeout, or interruption and records unsuccessful cleanup. Loss of connectivity can
prevent immediate deletion. Preserve the report's IDs, retry cleanup when reachable,
and rely on the server-enforced sandbox TTL as the final resource-lifetime bound.
JSON is the canonical report; a crash between the two atomic file replacements can
leave Markdown one revision behind JSON.

## Reporting a vulnerability

Use GitHub's private vulnerability reporting for the repository when available.
If it is unavailable, open a public issue containing only a request for a private
contact channel. Do not publish exploit details, credentials, or customer material.
Include the TriageLab revision, pinned kagent/Substrate versions, a sanitized minimal
reproduction, and the security boundary affected when reporting privately.

Evidence byte limits cap downloaded report content, not the amount a process can
write inside its sandbox. Disk/snapshot exhaustion is still possible; keep the
development VM disposable and monitor its free storage. Sandbox TTL does not
expire investigator Sessions; their separate idle TTL and explicit cleanup apply.
