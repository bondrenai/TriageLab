# GitLab issues

TriageLab reads issues from one GitLab project and prepares them for
investigation. Each issue goes through the first matching procedure: the
built-in Python 3.12 timeout procedure, or one an admin adds. The workspace
provides preview, tracking, reviewed clarification notes, and an evidence
checklist. Completed investigations can publish a reviewed comment to the
linked issue. GitLab remains the issue and conversation record.

GitLab issues are looked up by their per-project number (the iid) or a
pasted issue URL; both map to the numeric iid, which stays the case key.
The connector uses the documented GitLab REST v4 API as reviewed on
2026-11-11: issue reads, issue notes with system and internal notes
excluded from evidence, note writes validated against GitLab's documented
body normalization (quick actions, carriage returns, trailing whitespace),
and attachment downloads through the project Markdown uploads endpoint
(`GET /projects/:id/uploads/:secret/:filename`, GitLab 17.4 and newer).
GitLab.com and self-managed instances are supported through one HTTPS
origin per connection.

## Configure a project

Copy [gitlab.example.json](../gitlab.example.json) to `gitlab.local.json`,
set `repository` to the project path (`group/project`, including
subgroups), set `origin` to your instance for self-managed GitLab (omit it
for GitLab.com), and set `policy.tag` to the GitLab label that selects
open issues for the built-in procedure. One connection watches one
project.

```json
{
  "source": "gitlab",
  "repository": "your-group/your-project",
  "origin": "https://gitlab.example.com",
  "token_env": "TRIAGELAB_GITLAB_TOKEN",
  "poll_seconds": 60,
  "investigation_comments": true,
  "policy": {
    "tag": "triagelab_python_timeout",
    "auto_questions": true,
    "max_questions": 2
  }
}
```

Create a personal access token for a dedicated account that can read the
project (scope `read_api`) and comment on its issues (scope `api` or
`write_api`) — the same token covers both when writes are enabled.
`token_env` names the environment variable holding the token — the token
itself never appears in configuration files, the browser, the
investigator, or reports — and is sent as the `PRIVATE-TOKEN` header. A
configured but missing token stops startup. The token is required: the
uploads download endpoint needs at least Guest access, so anonymous reads
are not supported. For read-only operation, omit `auto_questions` and
`investigation_comments`.

Start the workspace on the VM:

```sh
bin/triagelab serve --tickets gitlab.local.json --output reports/gitlab
```

Use an SSH tunnel to open `http://127.0.0.1:8080/tickets`; keep the server
on loopback. Configure kagent and its invocation credential before
starting investigations. Preview only reads the issue. Tracking a ready
issue can start a run when the procedure enables automatic launch.

Use a separate report directory for each project and for other sources.
Preserve its private `.tickets` directory across restarts: it stores
tracked cases, approval state, clarification and summary delivery
reservations and receipts, and consumed run attempts. Changing the project
or origin requires a separate directory. Restart the server after
changing configuration or replacing its environment token.

Connections can also be created in the workspace UI. On the
**Connections** page an admin adds a GitLab connection with the project
path and optional instance origin, then either pastes the access token
(stored in the private connection registry) or names the environment
variable that holds it.

## Prepare evidence

**Edit procedures** in Ticket cases lets you set up a procedure for each
label with its own evidence requirements, clarification, agent, and
sandbox. The first matching procedure applies. Use **Preview draft on
issue** to check an individual procedure before saving. See
[procedure editing](tickets.md#clarify-and-approve).

Enter the GitLab issue number or paste the issue URL, and choose
**Preview issue**. The case reference is always the numeric issue number
once resolved. Preview reads the issue without enrolling it. Choose
**Track issue** to keep its preparation state. Only issues in the
`opened` state are open for investigation; closed issues and work item
types other than plain issues (incidents, tasks, test cases,
requirements) are excluded.

The procedure takes configured facts and files from the requester's
visible notes and the issue description. System notes (label changes,
milestones, assignments) and internal notes never supply evidence. For
the starter Python timeout checklist, provide these fields as individual
lines:

```text
Python version: 3.12.4
Expected: The health check allows five seconds for a response.
Actual: The deadline expires after five milliseconds.
```

Attach sanitized files that meet the procedure's attachment rules to the
issue description or a note as Markdown uploads. Supported files are
`.py`, `.json`, `.txt`, and `.log`. Uploads download through the
authorized project uploads endpoint on the configured origin — the token
never travels anywhere else, and an unexpected response shape fails
closed and requires review. Instances older than GitLab 17.4 do not serve
that endpoint; upgrade the instance or review the file manually.
TriageLab restores `.py.txt`-style double suffixes. Names must be unique
after this normalization. At most four files, each no larger than 1 MiB,
can enter a run.

Issue descriptions and note bodies are Markdown and stay inert imported
data. The first matching required attachment rule, or the first matching
optional rule, selects the latest requester note with that attachment.
Other attachment rules can fill gaps from separate notes. Review the
source references and SHA-256 hashes before approval.

## Clarify and approve

With `auto_questions: true`, track the issue and review its requester,
matching label policy, and exact draft. **Approve and send clarification**
posts that note and authorizes narrower follow-ups to the same requester
under the reviewed policy. The note is visible to everyone with access to
the issue and its project — including the requester on confidential
issues. GitLab's newer internal notes are never sent by the connector. A
change to the requester or policy requires fresh approval.

Subsequent refreshes can send a follow-up when the newest visible note is
a reply from the requester. `max_questions` must be 1–3 when automatic
questions are enabled and caps total send attempts per issue. Identical
questions are not repeated; a reply with no progress requires review.
Notes posted by the configured access token carry its account, so they
can never be mistaken for requester replies unless the same account
authored the issue; the note anchor URL still tells a posted
clarification apart. If a clarification remains unanswered for seven
days, the next check routes the issue for review.

Set `poll_seconds` to 60–3600 to check one eligible tracked issue per
interval, in turn. Set it to `0` to refresh manually. **Refresh and apply
policy** can also check a tracked issue immediately; **Pause tracking**
stops its automatic work. Tracking does not enumerate a GitLab issue list
or run saved filters. With `auto_questions: false`, review the draft,
post it manually in GitLab, and choose **Refresh issue** after the
requester replies. Waiting and drafting do not allocate a sandbox or call
a model.

Every send is reserved in durable case state first. A confirmed receipt
records the GitLab note id and is validated against the returned note —
including GitLab's documented body normalization, so a receipt whose body
does not round-trip to the sent message is treated as unconfirmed. A
timeout, unexpected response, or missing receipt leaves delivery
unconfirmed across restarts. Inspect the issue in GitLab; TriageLab does
not automatically retry an uncertain send. Before sending, the connector
rechecks the issue and conversation. GitLab does not support conditional
writes for notes, so another edit can still occur between that check and
the send. This is not an atomic compare-and-send guarantee like Zendesk's
safe update.

A procedure can start automatically when the evidence is ready.
[Automatic ticket investigations](https://bondrenai.github.io/TriageLab/guides/workspace/#automatic-ticket-investigations).

For the default review path, choose **Approve and start investigation**.
The plan binds approval to the selected evidence and run settings and
expires after 15 minutes. TriageLab rereads the issue before launch;
changed evidence requires fresh review. Each tracked issue can consume
one investigation attempt and shares the workspace's single active run
slot. Reports retain the GitLab origin, project, and issue references
alongside collected process evidence.

## Share an investigation summary

Set `investigation_comments: true` to enable summaries for completed
linked investigations. On the report, choose **Review investigation
comment**. Review the linked issue, the project audience, and the exact
body; you can edit the prepared text before choosing **Post investigation
comment**, and GitLab receives the exact edited body. The preview includes
the run ID, outcome, a bounded agent summary, and selected process IDs,
commands, and exit codes. Model analysis remains a hypothesis; the
process records are the collected evidence.

A procedure policy can also post the comment without a reviewer: set
`auto_comments: true` on a saved procedure (it requires
`investigation_comments`). When such a run completes, TriageLab posts the
prepared comment itself; if automatic posting cannot run, the preview stays
ready for a human to review, edit, and post.

The summary is posted as a fenced plain-text note visible to everyone
with access to the issue and its project; internal notes are not offered.
A confirmed send stores its note ID; repeating approval returns that
receipt. An uncertain delivery keeps its durable reservation and blocks
another send for the same investigation. Use the run ID to inspect the
issue conversation in GitLab.

## Limits

The connector supports one project per GitLab connection, on GitLab.com
or a self-managed instance reached over HTTPS. Cases are keyed by the
per-project issue number (iid); lookups accept the number or the issue
URL. GitLab priority and custom-field policy, issue transitions, saved
issue lists, epics, merge requests, work item types other than plain
issues, and internal notes are unsupported. An issue's total note count —
including system notes — is capped at 100; more than that, oversized
responses, unsupported files, or inaccessible evidence require manual
review. Note bodies are compared after GitLab's documented
normalization. The uploads download endpoint requires GitLab 17.4 or
newer. Internal notes added by the team are imported as non-public and
never supply evidence. GitLab API rate limits can temporarily prevent
refresh.

Imported prose and files remain untrusted. Attachment validation and
credential redaction are best effort; review evidence for secrets and
customer data before sending it to a model. The existing
[security model](../SECURITY.md), alpha kagent API limitations, output
bounds, watchdog latency, and cleanup reporting also apply.

Live checks ran on 2026-11-12 against a disposable self-managed GitLab CE
19.5.5 instance: connection verification, URL reference resolution,
Markdown upload download through real evidence preparation, system-note
exclusion, investigation-comment delivery with a verified receipt, and
clarification delivery with one bounded follow-up after a requester reply.
The run is repeatable with the opt-in tests in
`internal/ticketflow/gitlab_live_test.go` by pointing
`TRIAGELAB_GITLAB_LIVE_ORIGIN`, `_TOKEN`, and `_GROUP` at a disposable
instance. Still unexercised live: GitLab.com, a model-backed investigation
approved from a GitLab case (requires kagent), internal notes (Premium;
never sent), and confidential-issue requester visibility.
