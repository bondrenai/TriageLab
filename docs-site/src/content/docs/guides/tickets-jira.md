---
title: Jira issues
description: Prepare issues from one Jira Cloud site with reviewed clarifications, and internal notes.
---

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

Copy `jira.example.json` to `jira.local.json`, set `origin` to your site's
`https://your-site.atlassian.net` origin, set `email` to the Atlassian
account that owns the API token, and set `policy.tag` to the Jira label that
selects open issues:

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
projects and comment on their issues; the account email and token authorize
together as HTTP basic authentication. `token_env` names the environment
variable holding the token — the token itself never appears in configuration
files, the browser, the investigator, or reports. A configured but missing
token stops startup. For read-only operation set `auto_questions: false` and
`internal_notes: false`; the token is still required. Investigation summaries
post as internal comments — public investigation comments are not offered for
Jira — and the Zendesk priority and custom-field policy settings do not
apply.

Start the workspace on the VM:

```sh
bin/triagelab serve --tickets jira.local.json --output reports/jira
```

Use an SSH tunnel to open `http://127.0.0.1:8080/tickets`. One workspace has
one source and one site; use a separate report directory per site and
preserve its private `.tickets` directory across restarts.

Admins can also add Jira connections in the workspace UI on the
**Connections** page, pasting the API token or naming the environment variable
that holds it.

## Prepare evidence and launch

**Edit procedures** in Ticket cases sets up a procedure per label with its own
evidence requirements, clarification policy, agent, and sandbox; the first
matching procedure applies. Preview an issue by its key, a pasted issue URL,
or its numeric id — the case reference is always the numeric issue id once
resolved. Preview reads without enrolling; **Track issue** keeps its
preparation state. Issues must sit in a status whose status category is
to-do or in-progress; done-category issues and issues without a reporter are
excluded.

The procedure takes configured facts and files from the requester's visible
comments and the issue description; internal notes on Jira Service Management
issues and comments restricted to a role or group never supply evidence. For
the starter checklist the requester provides `Python version:`, `Expected:`,
and `Actual:` lines. Descriptions and comment bodies are Atlassian Document
Format; the connector renders their text and collapses formatting, mentions,
inline media, and tables to plain text. At most four sanitized files (`.py`,
`.json`, `.txt`, `.log`) of 1 MiB each enter a run; attachments download
through the authorized Jira content endpoint, whose single signed redirect to
Atlassian's media host is followed without the site credential and fails
closed on an unexpected shape. Review source references and hashes before
approval.

With `auto_questions: true`, review the requester, label policy, and exact
draft before **Approve and send clarification**; on Jira Service Management
issues the comment is explicitly customer-visible, and on Jira Software
issues it is an ordinary comment visible to everyone with access to the
issue. Visibility is set explicitly on every send through the
`sd.public.comment` entity property and never follows a project's default.
Follow-ups are authorized only to the same requester under the reviewed
policy; `max_questions` (1–3) caps send attempts, identical questions are not
repeated, and an unanswered clarification routes the issue for review after
seven days. Comments posted by the configured token carry its account and
never count as requester replies unless the same account authored the issue.
Every send is reserved in durable case state first; a confirmed comment is
identified by ID and never resent, while an uncertain delivery stays held
for manual inspection.

A procedure can start automatically when the evidence is ready.
[Automatic ticket investigations](/TriageLab/guides/workspace/#automatic-ticket-investigations).

For the default review path, **Approve and start investigation** binds approval to
the selected evidence and run settings, expires after 15 minutes, and rereads
the issue before launch. Each tracked issue consumes one investigation
attempt and shares the workspace's single run slot.

## Share an investigation summary

With `internal_notes: true`, choose **Review internal note** on a completed
report. The preview shows the run ID, outcome, a bounded agent summary, and
selected process IDs, commands, and exit codes; the note posts as an internal
comment — customer-invisible on Jira Service Management issues. Reviewers can
edit the prepared text before posting — Jira receives the exact edited body.
A confirmed send stores its comment ID; an uncertain delivery keeps its
reservation and blocks another send for the same investigation.

A procedure policy with `auto_comments: true` (requires `internal_notes`)
posts the note without a reviewer once the run completes; if automatic
posting cannot run, the preview stays ready for a human to review, edit, and
post.

## Limits

One site per workspace instance, and only `atlassian.net` site origins; Jira
Data Center deployments are unsupported. Cases are keyed by the numeric Jira
issue id. Jira priority and custom-field policy, issue transitions, saved
filters, JQL, project restriction beyond the token's own permissions, and
inline rich-content rendering are unsupported. More than a bounded number of
visible comments, oversized responses, or inaccessible evidence require
manual review; Jira API rate limits can temporarily prevent refresh. The
signed attachment redirect shape is an observed transport behavior, not a
versioned REST contract; a changed media host or URL shape fails closed and
requires review. Imported prose and files remain untrusted — review evidence
for secrets before sending it to a model. A controlled live pilot against a
real Jira Cloud site completed 2026-11-11, covering lookups, ADF import with
a live signed attachment download, clarification delivery with a bounded
follow-up, a model-backed two-round investigation approved from a Jira case,
and internal-note delivery; `sd.public.comment` internal visibility on a
Jira Service Management project remains to be exercised.
