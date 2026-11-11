# Prepare Linear issues for investigation

TriageLab can read issues from one team in one Linear workspace and prepare
them for the existing Python 3.12 timeout investigation procedure. The
workspace provides preview, tracking, reviewed clarification comments,
and an evidence checklist. Completed investigations
can also publish a reviewed summary to the linked issue. Linear remains the
issue and conversation record.

## Configure a team

Build the CLI and browser assets on your development VM as described in
[UI setup](ui.md). Copy [linear.example.json](../linear.example.json) to
`linear.local.json`, set `workspace` to your `linear.app` URL key (the first
path segment of your Linear URLs), set `team` to the team key (the uppercase
prefix of issue identifiers such as `ENG-12`), and set `policy.tag` to the
Linear label that selects open issues for this procedure. One workspace has
one source and one team.

Create a [personal API key](https://linear.app/settings/api) in
**Settings → Security & access → API**. Reads use the Linear GraphQL API
[`team`](https://linear.app/developers/graphql) and `issue` queries with the
issue's per-team `number`; posting clarifications and summaries uses the
`commentCreate` mutation. Set `TRIAGELAB_LINEAR_TOKEN` on the server through
your secret manager or a hidden shell prompt before starting TriageLab. The
`token_env` setting names this environment variable; it never contains the
key itself. Keep the key out of configuration files and issue evidence. It
stays server-side and is excluded from the browser, investigator, and
reports. Linear uses this configured key; there is no Linear OAuth connection
flow. Linear has no anonymous access, so a configured but missing or
malformed key stops startup; credential or permission errors are reported
when accessing an issue or posting a comment.

For read-only operation, set `auto_questions: false`, `max_questions: 0`, and
`investigation_comments: false`; the API key is still required. The key acts
as your Linear account, so use a dedicated service account that can read the
team and post comments. `internal_notes` must stay false or be omitted:
Linear has no private issue comments, and enabling that Zendesk setting is
rejected. Zendesk priority and custom-field policy settings do not apply.

Start the workspace on the VM:

```sh
bin/triagelab serve --tickets linear.local.json --output reports/linear
```

Use an SSH tunnel to open `http://127.0.0.1:8080/tickets`; keep the server on
loopback. Configure kagent and its invocation credential before starting
investigations. Preview only reads the issue. Tracking a ready issue can
start a run when the procedure enables automatic launch.

Use a separate report directory for each team and for Zendesk or GitHub
sources. Preserve its private `.tickets` directory across restarts: it stores
tracked cases, approval state, clarification and summary delivery
reservations and receipts, and consumed run attempts. Changing the workspace
or team requires a separate directory. Restart the server after changing
configuration or replacing its environment key.

## Prepare evidence

**Edit procedures** in Ticket cases lets you set up a procedure for each
product label with its own evidence requirements, clarification, agent, and
sandbox. The first matching procedure applies. Use **Preview draft on issue**
to check an individual procedure before saving. See
[procedure editing](tickets.md#clarify-and-approve).

Enter the issue number within the configured team (the digits of `ENG-12`)
and choose **Preview issue**. Preview reads the issue without enrolling it.
Choose **Track issue** to keep its preparation state. An issue must be in a
backlog, unstarted, or started workflow state; completed and canceled issues
are excluded. Linear priority filtering is not supported.

The procedure takes configured facts and files from the issue creator's
original description and subsequent comments. Other participants' replies do
not supply evidence. For the starter Python timeout checklist, provide these
fields as individual lines:

```text
Python version: 3.12.4
Expected: The health check allows five seconds for a response.
Actual: The deadline expires after five milliseconds.
```

Attach sanitized files that meet the procedure's attachment rules to the
issue description or the creator's comments. Supported files are `.py`,
`.json`, `.txt`, and `.log`, uploaded through Linear's own attach control so
they render as Markdown links to `uploads.linear.app` or
`linearusercontent.com`. TriageLab restores `.py.txt`-style double suffixes.
Names must be unique after this normalization. At most four files, each no
larger than 1 MiB, can enter a run.

The first matching required attachment rule, or the first matching optional
rule, selects the latest creator message with that attachment. Other
attachment rules can fill gaps from separate messages. Review the source
references and SHA-256 hashes before approval.

## Clarify and approve

With `auto_questions: true`, track the issue and review its creator, matching
label policy, and exact draft. **Approve and send clarification** posts that
comment and authorizes narrower follow-ups to the same creator under the
reviewed policy. Comments are visible to everyone with access to the
workspace, and Linear applies its notification rules. A change to the creator
or policy requires fresh approval.

Subsequent refreshes can send a follow-up when the newest conversation message
is a reply from the creator. `max_questions` must be 1–3 when automatic
questions are enabled and caps total send attempts per issue. Identical
questions are not repeated; a reply with no progress requires review.
Confirmed outgoing comments are identified by their Linear comment IDs and
never count as creator replies, including when the issue creator and the
account behind the key are the same. If a clarification remains unanswered
for seven days, the next check routes the issue for review.

Set `poll_seconds` to 60–3600 to check one eligible tracked issue per interval, in
turn. Set it to `0` to refresh manually. **Refresh and apply policy** can
also check a tracked issue immediately; **Pause tracking** stops its automatic
work. Tracking does not enumerate a team inbox. With
`auto_questions: false`, review the draft, post it manually in Linear, and
choose **Refresh issue** after the creator replies. Waiting and drafting do
not allocate a sandbox or call a model.

Every send is reserved in durable case state first. A confirmed receipt
records the Linear comment ID; confirmed comments are not resent. A timeout,
unexpected response, or missing receipt leaves delivery unconfirmed across
restarts. Inspect the issue in Linear; TriageLab does not automatically retry
an uncertain send. Before sending, the connector rechecks the issue and
comments. Linear does not support conditional writes for comments, so another
edit can still occur between that check and the send. This is not an atomic
compare-and-send guarantee like Zendesk's safe update.

A procedure can start automatically when the evidence is ready.
[Automatic ticket investigations](https://bondrenai.github.io/TriageLab/guides/workspace/#automatic-ticket-investigations).

For the default review path, choose **Approve and start investigation**. The plan
binds approval to the selected evidence and run settings and expires after
15 minutes. TriageLab rereads the issue before launch; changed evidence
requires fresh review. Each tracked issue can consume one investigation
attempt and shares the workspace's single active run slot. Reports retain the
Linear team and issue references alongside collected process evidence.

## Share an investigation summary

Set `investigation_comments: true` to enable summaries for completed linked
investigations. On the report, choose **Review comment**. Review the linked
issue, workspace audience, and exact body before choosing **Post comment**.
The preview includes the run ID, outcome, a bounded agent summary, and
selected process IDs, commands, and exit codes. Its imported text is wrapped
in a literal Markdown code block to prevent accidental mentions, links, or
images. Model analysis remains a hypothesis; the process records are the
collected evidence.

The summary is an ordinary issue comment, visible to everyone with workspace
access. It is not a private internal note. A confirmed send stores its
comment ID; repeating approval returns that receipt. An uncertain delivery
keeps its durable reservation and blocks another send for the same
investigation. Use the run ID to inspect the Linear conversation.

## Limits

The connector supports one team of one `linear.app` workspace. Attachments
must be Linear uploads referenced by Markdown links in the description or
comments; expired signed upload URLs, inline code blocks as files, and
arbitrary external links are unsupported. Upload URLs are requested as
short-lived signed links and downloaded without the API key; the signed URLs
themselves are kept out of reports. Issues are identified by their per-team
number, and issues from other teams with the same number are never matched.
Priority and custom-field policy, Linear projects, sub-issues, inbox
synchronization, and other workflow states are unsupported. More than 100
issue comments, oversized responses, unsupported files, or inaccessible
evidence require manual review. Linear API rate limits can temporarily
prevent refresh.

Imported prose and files remain untrusted. Attachment validation and
credential redaction are best effort; review evidence for secrets and
customer data before sending it to a model. The existing
[security model](../SECURITY.md), alpha kagent API limitations, output
bounds, watchdog latency, and cleanup reporting also apply. Connector
fixtures exercise parsing, delivery, and security boundaries; they do not
establish a live Linear-to-kagent investigation demonstration. No live
Linear clarification or summary sends were performed while implementing this
connector; a controlled live pilot remains to be run.
