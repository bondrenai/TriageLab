# Security model and limitations

TriageLab runs commands selected by a model and ingests untrusted technical reports.
Use a dedicated, disposable deployment and identity. This proof of concept is not
a multi-tenant service or a production incident-remediation tool.

## Execution boundary

The CLI never executes imported application code or agent-generated commands on
its host. It uploads selected files into a standalone KAgent Sandbox. Agent Substrate
provides the gVisor boundary; a configured microVM worker pool is an optional stronger
isolation backend. The demonstration requires no host Docker socket, filesystem
mount, privileged interface, or production credential inside that sandbox.

The supplied SandboxTemplate has no egress policy references. KAgent/Substrate deny
outbound connections by default, including package downloads. Preinstall dependencies
in a reviewed, digest-pinned tools image, or explicitly review and grant narrowly
scoped egress through upstream policies. Do not enable broad egress just to make an
experiment pass. Images and the trusted Substrate host components remain part of
the supply-chain and isolation trust boundary.

CPU, memory, and maximum sandbox lifetime are configured on the upstream installation.
The CLI also limits investigation duration, observed experiment count, process runtime,
input sizes, and collected output. Client watchdogs act after observing upstream state;
they are not precise server-side command or tool-call quotas. A process may exceed its
client limit by polling and request latency. Use upstream resource limits and TTL as
the backstop if the CLI crashes, loses connectivity, or is killed without cleanup.

## Identity and credentials

The example installation deliberately uses KAgent's development authentication mode.
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
inventory. Provider credentials belong to KAgent's model/egress configuration, not uploaded files or
SandboxTemplate environment variables. TriageLab reads its API token from a named
environment variable and does not place it in the reproduction environment. Caller
identity propagation is required so the agent and CLI can access the same sandbox.

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

The CLI retrieves process state and output independently from KAgent. This confirms
the recorded execution results came from the service, not that arbitrary output is
true or that a model's causal explanation is correct. Reports distinguish observed
experiments from hypotheses, retain failures, and do not assert confirmed root cause.

Local ingestion refuses symlinks, special files, obvious credential filenames and
patterns, unsafe destination names, and oversized inputs. It excludes version-control
and common generated/vendor directories. These rules are intentionally conservative,
and secret detection is incomplete. Review selected files before submitting a case.
Support text and relevant file/output contents may be sent to the configured model
provider; choose a provider and retention configuration appropriate for that data.

Reports use private file/directory permissions, atomic replacement of each file,
best-effort secret redaction, and fenced rendering of untrusted Markdown. They can
still contain sensitive support data. Redaction is not a guarantee, and rendered
reports should be reviewed before sharing. Upstream logs, model-provider retention,
snapshots, and backups have their own retention policies; deleting a session or
sandbox does not prove all historical data has been erased.

## Browser workspace

The browser workspace is a local, single-user interface. Its server refuses
non-loopback listeners, validates request hosts and origins, and requires a
custom request header for mutations. These checks reduce cross-site requests;
they are not user authentication. Anyone with access to the local port can read
reports and start investigations. Use an SSH tunnel for a remote VM, never a
public proxy. The browser receives redacted reports, not the configured API
token. Selected uploads use the same ingestion limits and credential screening
as CLI inputs.

With the optional [Zendesk procedure](docs/tickets.md), the server can add
public clarifications to explicitly tracked tickets after the operator approves
the first draft and requester. Public replies use fixed templates. Optional
internal notes contain a reviewed, bounded summary and selected evidence from
a completed linked run. The ticket record journals comment attempts before
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
links, or images. Confirmed outgoing clarification IDs are excluded from author
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
Include the TriageLab revision, pinned KAgent/Substrate versions, a sanitized minimal
reproduction, and the security boundary affected when reporting privately.

Evidence byte limits cap downloaded report content, not the amount a process can
write inside its sandbox. Disk/snapshot exhaustion is still possible; keep the
development VM disposable and monitor its free storage. Sandbox TTL does not
expire investigator Sessions; their separate idle TTL and explicit cleanup apply.
