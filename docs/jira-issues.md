# Jira issues

TriageLab reads issues from one Jira Cloud site and prepares them for
investigation. Each issue goes through the first matching procedure: the
built-in Python 3.12 timeout procedure, or one an admin adds. The workspace
provides preview, tracking, reviewed clarification comments, and an evidence
checklist. Completed investigations can publish a reviewed internal note to
the linked issue. Jira remains the issue and conversation record.

Jira issues are looked up by an issue key (`PROJ-123`), a pasted issue URL,
or the numeric issue id. All three map to the numeric Jira issue id, which
stays the case key even when the issue later moves to another project and
its key changes. The connector uses the documented Jira Cloud REST v3 API as
reviewed on 2026-11-11: issue reads, issue comments, comment writes with
explicit `sd.public.comment` visibility, and attachment downloads through the
authorized content endpoint's single signed redirect. Jira Data Center
(self-managed) deployments are a different API vintage and are not supported.

## Configure a site

Build the CLI and browser assets on your development VM as described in
[UI setup](ui.md). Copy [jira.example.json](../jira.example.json) to
`jira.local.json`, set `origin` to your site's `https://your-site.atlassian.net`
origin, set `email` to the Atlassian account that owns the API token, and set
`policy.tag` to the Jira label that selects open issues for the built-in
procedure. One workspace has one source and one site.

```json
{
  "source": "jira",
  "origin": "https://your-site.atlassian.net",
  "email": "triagelab@your-company.example",
  "token_env": "TRIAGELAB_JIRA_TOKEN",
  "poll_seconds": 60,
  "internal_notes": true,
  "policy": {
    "tag": "triagelab_python_timeout",
    "auto_questions": true,
    "max_questions": 2
  }
}
```

Create an API token at id.atlassian.com under **Settings → Security → API
tokens** for a dedicated service account that can browse the relevant
projects and comment on their issues. `token_env` names the environment
variable holding the token — the token itself never appears in configuration
files, the browser, the investigator, or reports. The account email and
token authorize together as HTTP basic authentication against the site. A
configured but missing token stops startup. For read-only operation, omit
`auto_questions` and `internal_notes`.

Start the workspace on the VM:

```sh
bin/triagelab serve --tickets jira.local.json --output reports/jira
```

Use an SSH tunnel to open `http://127.0.0.1:8080/tickets`; keep the server on
loopback. Configure kagent and its invocation credential before starting
investigations. Preview only reads the issue. Tracking a ready issue can
start a run when the procedure enables automatic launch.

Use a separate report directory for each site and for Zendesk, GitHub,
Linear, or Bondren sources. Preserve its private `.tickets` directory across
restarts: it stores tracked cases, approval state, clarification and summary
delivery reservations and receipts, and consumed run attempts. Changing the
site requires a separate directory. Restart the server after changing
configuration or replacing its environment token.

Connections can also be created in the workspace UI. On the **Connections**
page an admin adds a Jira connection with the site origin and account email,
then either pastes the API token (stored in the private connection registry)
or names the environment variable that holds it.

## Prepare evidence

**Edit procedures** in Ticket cases lets you set up a procedure for each
label with its own evidence requirements, clarification, agent, and sandbox.
The first matching procedure applies. Use **Preview draft on issue** to check
an individual procedure before saving. See
[procedure editing](tickets.md#clarify-and-approve).

Enter the Jira issue key, a pasted issue URL, or the numeric issue id, and
choose **Preview issue**. The case reference is always the numeric issue id
once resolved. Preview reads the issue without enrolling it. Choose **Track
issue** to keep its preparation state. An issue must sit in a status whose
status category is to-do or in-progress; done-category issues are excluded.
Issues without a reporter are rejected. Jira priority filtering and
custom-field policy are not supported.

The procedure takes configured facts and files from the requester's visible
comments and the issue description. Internal notes on Jira Service Management
issues and comments restricted to a role or group never supply evidence. For
the starter Python timeout checklist, provide these fields as individual
lines:

```text
Python version: 3.12.4
Expected: The health check allows five seconds for a response.
Actual: The deadline expires after five milliseconds.
```

Attach sanitized files that meet the procedure's attachment rules to the
issue. Supported files are `.py`, `.json`, `.txt`, and `.log`. Attachments
download through the authorized Jira content endpoint, which answers with one
signed redirect to Atlassian's media host; the site credential never travels
to that host and the signed URL itself is the grant. A changed host or shape
fails closed and requires review. TriageLab restores `.py.txt`-style double
suffixes. Names must be unique after this normalization. At most four files,
each no larger than 1 MiB, can enter a run.

Issue descriptions and comment bodies are Atlassian Document Format. The
connector renders their text; formatting, mentions, inline media, and tables
collapse to plain text. The first matching required attachment rule, or the
first matching optional rule, selects the latest requester comment with that
attachment. Other attachment rules can fill gaps from separate comments.
Review the source references and SHA-256 hashes before approval.

## Clarify and approve

With `auto_questions: true`, track the issue and review its requester,
matching label policy, and exact draft. **Approve and send clarification**
posts that comment and authorizes narrower follow-ups to the same requester
under the reviewed policy. On Jira Service Management issues the comment is
explicitly customer-visible; on Jira Software issues it is an ordinary
comment visible to everyone with access to the issue. Visibility is set
explicitly on every send through the `sd.public.comment` entity property and
never follows a project's default. A change to the requester or policy
requires fresh approval.

Subsequent refreshes can send a follow-up when the newest visible comment is
a reply from the requester. `max_questions` must be 1–3 when automatic
questions are enabled and caps total send attempts per issue. Identical
questions are not repeated; a reply with no progress requires review.
Comments posted by the configured API token carry its account, so they can
never be mistaken for requester replies unless the same account authored the
issue. If a clarification remains unanswered for seven days, the next check
routes the issue for review.

Set `poll_seconds` to 60–3600 to check one eligible tracked issue per interval, in
turn. Set it to `0` to refresh manually. **Refresh and apply policy** can
also check a tracked issue immediately; **Pause tracking** stops its
automatic work. Tracking does not enumerate a Jira queue or run saved
filters. With `auto_questions: false`, review the draft, post it manually in
Jira, and choose **Refresh issue** after the requester replies. Waiting and
drafting do not allocate a sandbox or call a model.

Every send is reserved in durable case state first. A confirmed receipt
records the Jira comment id and is validated against the returned comment.
A timeout, unexpected response, or missing receipt leaves delivery
unconfirmed across restarts. Inspect the issue in Jira; TriageLab does not
automatically retry an uncertain send. Before sending, the connector
rechecks the issue and conversation. Jira does not support conditional
writes for comments, so another edit can still occur between that check and
the send. This is not an atomic compare-and-send guarantee like Zendesk's
safe update.

A procedure can start automatically when the evidence is ready.
[Automatic ticket investigations](https://bondrenai.github.io/TriageLab/guides/workspace/#automatic-ticket-investigations).

For the default review path, choose **Approve and start investigation**. The plan
binds approval to the selected evidence and run settings and expires after
15 minutes. TriageLab rereads the issue before launch; changed evidence
requires fresh review. Each tracked issue can consume one investigation
attempt and shares the workspace's single active run slot. Reports retain
the Jira site and issue references alongside collected process evidence.

## Share an investigation summary

Set `internal_notes: true` to enable summaries for completed linked
investigations. On the report, choose **Review internal note**. Review the
linked issue, the internal audience, and the exact body before choosing
**Post internal note**. The preview includes the run ID, outcome, a bounded
agent summary, and selected process IDs, commands, and exit codes. Model
analysis remains a hypothesis; the process records are the collected
evidence.

The summary is posted as an internal comment (customer-invisible on Jira
Service Management issues). A confirmed send stores its comment ID; repeating
approval returns that receipt. An uncertain delivery keeps its durable
reservation and blocks another send for the same investigation. Use the run
ID to inspect the issue conversation in Jira.

## Limits

The connector supports one Jira Cloud site per workspace. Cases are keyed by
the numeric Jira issue id; lookups accept the issue key, a pasted issue URL,
or the id. Only `atlassian.net` site origins are accepted. Jira priority and
custom-field policy, issue transitions, saved filters, JQL, project
restriction beyond the token's own permissions, inline rich-content
rendering, and Data Center deployments are unsupported. An issue's visible
comment count is capped at 100; more than that, oversized responses,
unsupported files, or inaccessible evidence require manual review. Jira API
rate limits can temporarily prevent refresh.

The signed attachment redirect shape is an observed transport behavior, not
a versioned REST contract; a changed media host or URL shape fails closed
and requires review. Imported prose and files remain untrusted. Attachment
validation and credential redaction are best effort; review evidence for
secrets and customer data before sending it to a model. The existing
[security model](../SECURITY.md), alpha kagent API limitations, output
bounds, watchdog latency, and cleanup reporting also apply. Connector
fixtures exercise parsing, delivery, and security boundaries. A controlled
live pilot against a real Jira Cloud site completed on 2026-11-11:
key/URL/id lookups, ADF import including a live attachment download through
the signed media redirect, clarification delivery with a bounded follow-up
after a requester reply, a model-backed two-round investigation approved
from a Jira case, and internal-note delivery with its receipt were all
exercised end to end. The pilot ran on a plain Jira Software project, where
internal notes are ordinary project-visible comments; the
`sd.public.comment` internal visibility and a Jira Service Management
requester inbox remain to be exercised on a JSM project. The pilot also
surfaced that Jira assigns issue ids and comment ids from separate
sequences that collide numerically; the connector derives the issue
description's comment key instead of reusing the issue id.

