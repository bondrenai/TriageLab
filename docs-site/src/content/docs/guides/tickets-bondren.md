---
title: Bondren issues
description: Prepare issues from one Bondren workspace with reviewed clarifications, and internal notes.
---

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

Copy `bondren.example.json` to `bondren.local.json`, set `origin` to the
HTTPS origin serving both the Bondren API and its web workspace, and set
`policy.tag` to the Bondren tag that selects open issues:

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
but missing or malformed token stops startup. For read-only operation set
`auto_questions: false` and `internal_notes: false`; the token is still
required. Public investigation comments are not offered for Bondren —
summaries post as internal notes — and the Zendesk priority and custom-field
policy settings do not apply.

Start the workspace on the VM:

```sh
bin/triagelab serve --tickets bondren.local.json --output reports/bondren
```

Use an SSH tunnel to open `http://127.0.0.1:8080/tickets`. One workspace has
one source and one origin; use a separate report directory per origin and
preserve its private `.tickets` directory across restarts.

## Prepare evidence and launch

**Edit procedures** in Ticket cases sets up a procedure per tag with its own
evidence requirements, clarification policy, agent, and sandbox; the first
matching procedure applies. Preview an issue by its ID (from the issue URL),
a pasted issue URL, or its workspace number — the case reference is always
the issue number once resolved. Preview reads without enrolling; **Track
issue** keeps its preparation
state. Issues must be in a `new`, `on_you`, `on_customer`, or `on_hold`
state. Issues that Bondren mirrors from a connected source are rejected;
their conversation of record is the connected source.

The procedure takes configured facts and files from the requester's public
messages; internal notes and other teammates' messages never supply
evidence. For the starter checklist the requester provides
`Python version:`, `Expected:`, and `Actual:` lines. Bondren inspects
attachments before they are downloadable; only attachments reported as
`clean` are imported — pending or rejected files stay at the source. At most
four sanitized files (`.py`, `.json`, `.txt`, `.log`) of 1 MiB each enter a
run; review source references and hashes before approval.

With `auto_questions: true`, review the requester, tag policy, and exact
draft before **Approve and send clarification**; the message is visible to
the requester and everyone with access to the issue. Follow-ups are
authorized only to the same requester under the reviewed policy;
`max_questions` (1–3) caps send attempts, identical questions are not
repeated, and an unanswered clarification routes the issue for review after
seven days. Messages posted by the agent token are team-authored and never
count as requester replies. Every send is reserved in durable case state
first; a confirmed message is identified by ID and never resent, while an
uncertain delivery stays held for manual inspection.

A procedure can start automatically when the evidence is ready.
[Automatic ticket investigations](/TriageLab/guides/workspace/#automatic-ticket-investigations).

For the default review path, **Approve and start investigation** binds approval to
the selected evidence and run settings, expires after 15 minutes, and rereads
the issue before launch. Each tracked issue consumes one investigation
attempt and shares the workspace's single run slot.

## Share an investigation summary

With `internal_notes: true`, choose **Review internal note** on a completed
report. The preview shows the run ID, outcome, a bounded agent summary, and
selected process IDs, commands, and exit codes, wrapped so imported text
cannot create mentions or links. The note is an internal Bondren message —
visible to the team, not the requester. A confirmed send stores its message
ID; an uncertain delivery keeps its reservation and blocks another send for
the same investigation.

## Limits

One origin per workspace instance. Cases are keyed by the workspace-wide
issue number; a lookup accepts the issue ID, a pasted issue URL, or the
number (the number form needs Bondren's pending number-equality filter).
Only native issues are eligible; mirrored issues,
priority and custom-field policy, queues, environments, and saved-view
synchronization are unsupported. More than a bounded number of issue
messages, oversized responses, or inaccessible evidence require manual
review; Bondren API rate limits can temporarily prevent refresh. Imported
prose and files remain untrusted — review evidence for secrets before
sending it to a model. The connector is fixture-verified; a controlled live
Bondren pilot remains to be run.
