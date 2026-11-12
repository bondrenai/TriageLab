# Bondren issues

TriageLab reads issues from one Bondren workspace and prepares them for
investigation. Each issue goes through the first matching procedure: the
built-in Python 3.12 timeout procedure, or one an admin adds. The workspace
provides preview, tracking, reviewed clarification messages, and an evidence
checklist. Completed investigations can publish a reviewed internal note to
the linked issue. Bondren remains the issue and conversation record.

Bondren issues are looked up by their issue ID or their number. ID lookup
uses the by-ID endpoint and works against the Bondren API as reviewed on
2026-11-09; number lookup additionally uses the issue-number equality filter
on the saved-view query API (`POST /api/v1/issues/query`), which that review
found exposed only as a sort key — until Bondren ships the filter, reference
issues by their ID (the UUID segment of the Bondren issue URL). Everything
else the connector uses — issues, messages with public/internal visibility,
authorized clean attachment downloads, and agent-token bearers — exists
today.

## Configure a workspace

Build the CLI and browser assets on your development VM as described in
[UI setup](ui.md). Copy [bondren.example.json](../bondren.example.json) to
`bondren.local.json`, set `origin` to the HTTPS origin serving both the
Bondren API and its web workspace, and set `policy.tag` to the Bondren tag
that selects open issues for the built-in procedure. One workspace has one
source and one origin.

```json
{
  "source": "bondren",
  "origin": "https://app.bondren.example",
  "token_env": "TRIAGELAB_BONDREN_TOKEN",
  "poll_seconds": 60,
  "internal_notes": true,
  "policy": {
    "tag": "triagelab_python_timeout",
    "auto_questions": true,
    "max_questions": 2
  }
}
```

Create an agent token in **Settings → MCP Tokens**. Use a dedicated service
account that can read issues and post messages; the token acts as that
member. `token_env` names the environment variable holding the token — the
token itself never appears in configuration files, the browser, the
investigator, or reports. Bondren has no anonymous access, so a configured
but missing or malformed token stops startup. For read-only operation, omit
`auto_questions` and `internal_notes`.

Start the workspace on the VM:

```sh
bin/triagelab serve --tickets bondren.local.json --output reports/bondren
```

Use an SSH tunnel to open `http://127.0.0.1:8080/tickets`; keep the server on
loopback. Configure kagent and its invocation credential before starting
investigations. Preview only reads the issue. Tracking a ready issue can
start a run when the procedure enables automatic launch.

Use a separate report directory for each origin and for Zendesk, GitHub, or
Linear sources. Preserve its private `.tickets` directory across restarts: it
stores tracked cases, approval state, clarification and summary delivery
reservations and receipts, and consumed run attempts. Changing the origin
requires a separate directory. Restart the server after changing
configuration or replacing its environment token.

Connections can also be created in the workspace UI. On the **Connections**
page an admin adds a Bondren connection and either pastes the agent token
(stored in the private connection registry) or names the environment variable
that holds it.

## Prepare evidence

**Edit procedures** in Ticket cases lets you set up a procedure for each
product tag with its own evidence requirements, clarification, agent, and
sandbox. The first matching procedure applies. Use **Preview draft on issue**
to check an individual procedure before saving. See
[procedure editing](tickets.md#clarify-and-approve).

Enter the Bondren issue ID (the UUID segment of the issue URL), a pasted
issue URL, or the issue number, and choose **Preview issue**. The case
reference is always the issue number once resolved. Preview reads the issue
without enrolling it. Choose **Track issue** to keep its
preparation state. An issue must be in the `new`, `on_you`, `on_customer`,
or `on_hold` state; closed issues are excluded. Issues that Bondren mirrors
from a connected source (`operational_state: source_owned` or `replaying`)
are rejected — their conversation of record is the connected source, so
review and reply there instead. Bondren priority filtering is not supported.

The procedure takes configured facts and files from the requester's public
messages. Internal notes and other teammates' messages never supply
evidence. For the starter Python timeout checklist, provide these fields as
individual lines:

```text
Python version: 3.12.4
Expected: The health check allows five seconds for a response.
Actual: The deadline expires after five milliseconds.
```

Attach sanitized files that meet the procedure's attachment rules to the
requester's messages. Supported files are `.py`, `.json`, `.txt`, and
`.log`. Bondren inspects attachments before they are downloadable; only
attachments reported as `clean` are imported — pending, rejected, or
unavailable files stay at the source and count as missing evidence.
TriageLab restores `.py.txt`-style double suffixes. Names must be unique
after this normalization. At most four files, each no larger than 1 MiB, can
enter a run.

The first matching required attachment rule, or the first matching optional
rule, selects the latest requester message with that attachment. Other
attachment rules can fill gaps from separate messages. Review the source
references and SHA-256 hashes before approval.

## Clarify and approve

With `auto_questions: true`, track the issue and review its requester,
matching tag policy, and exact draft. **Approve and send clarification**
posts that public message and authorizes narrower follow-ups to the same
requester under the reviewed policy. The message is visible to the requester
and everyone with access to the issue, and Bondren applies its notification
rules. A change to the requester or policy requires fresh approval.

Subsequent refreshes can send a follow-up when the newest public message is
a reply from the requester. `max_questions` must be 1–3 when automatic
questions are enabled and caps total send attempts per issue. Identical
questions are not repeated; a reply with no progress requires review.
Messages posted by the configured agent token are team-authored, so they can
never be mistaken for requester replies. If a clarification remains
unanswered for seven days, the next check routes the issue for review.

Set `poll_seconds` to 60–3600 to check one eligible tracked issue per interval, in
turn. Set it to `0` to refresh manually. **Refresh and apply policy** can
also check a tracked issue immediately; **Pause tracking** stops its
automatic work. Tracking does not enumerate a workspace queue. With
`auto_questions: false`, review the draft, post it manually in Bondren, and
choose **Refresh issue** after the requester replies. Waiting and drafting
do not allocate a sandbox or call a model.

Every send is reserved in durable case state first. A confirmed receipt
records the Bondren message ID; confirmed messages are not resent. A
timeout, unexpected response, or missing receipt leaves delivery unconfirmed
across restarts. Inspect the issue in Bondren; TriageLab does not
automatically retry an uncertain send. Before sending, the connector
rechecks the issue and conversation. Bondren does not support conditional
writes for messages, so another edit can still occur between that check and
the send. This is not an atomic compare-and-send guarantee like Zendesk's
safe update.

A procedure can start automatically when the evidence is ready.
[Automatic ticket investigations](https://bondrenai.github.io/TriageLab/guides/workspace/#automatic-ticket-investigations).

For the default review path, choose **Approve and start investigation**. The plan
binds approval to the selected evidence and run settings and expires after
15 minutes. TriageLab rereads the issue before launch; changed evidence
requires fresh review. Each tracked issue can consume one investigation
attempt and shares the workspace's single active run slot. Reports retain
the Bondren origin and issue references alongside collected process
evidence.

## Share an investigation summary

Set `internal_notes: true` to enable summaries for completed linked
investigations. On the report, choose **Review internal note**. Review the
linked issue, the internal audience, and the exact body before choosing
**Post internal note**. The preview includes the run ID, outcome, a bounded
agent summary, and selected process IDs, commands, and exit codes. Its
imported text is wrapped in a literal Markdown code block to prevent
accidental mentions, links, or images. Model analysis remains a hypothesis;
the process records are the collected evidence.

The summary is an internal Bondren message, visible to the team but not the
requester. A confirmed send stores its message ID; repeating approval
returns that receipt. An uncertain delivery keeps its durable reservation
and blocks another send for the same investigation. Use the run ID to
inspect the issue conversation in Bondren.

## Limits

The connector supports one Bondren origin per workspace. Cases are keyed by
the workspace-wide issue number; a lookup accepts the issue ID, a pasted
issue URL, or the number (the number form needs Bondren's pending
number-equality filter). Only native issues are eligible;
issues mirrored from connected sources, and issues under migration replay,
must be handled at their source. Only `clean` attachments are imported.
Priority and custom-field policy, Bondren queues, environments, saved-view
synchronization, and message editing are unsupported. More than 100 issue
messages, oversized responses, unsupported files, or inaccessible evidence
require manual review. Bondren API rate limits can temporarily prevent
refresh.

Imported prose and files remain untrusted. Attachment validation and
credential redaction are best effort; review evidence for secrets and
customer data before sending it to a model. The existing
[security model](../SECURITY.md), alpha kagent API limitations, output
bounds, watchdog latency, and cleanup reporting also apply. Connector
fixtures exercise parsing, delivery, and security boundaries; they do not
establish a live Bondren-to-kagent investigation demonstration. No live
Bondren clarification or summary sends were performed while implementing
this connector; a controlled live pilot remains to be run.

