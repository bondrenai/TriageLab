---
title: Linear issues
description: Prepare issues from one Linear team with reviewed clarifications, and investigation summaries.
---

TriageLab reads issues from one team in one Linear workspace and prepares them
for investigation. Each issue goes through the first matching procedure: the
built-in Python 3.12 timeout procedure, or one an admin adds. The workspace
provides preview, tracking, reviewed clarification comments, and an evidence
checklist. Completed investigations can publish a reviewed summary to the
linked issue. Linear remains the issue and conversation record.

## Configure a team

Copy `linear.example.json` to `linear.local.json`, set `workspace` to your
`linear.app` URL key (the first path segment of your Linear URLs), set `team`
to the team key (the uppercase prefix of identifiers such as `ENG-12`), and
set `policy.tag` to the label that selects open issues:

```json
{
  "source": "linear",
  "workspace": "your-workspace",
  "team": "ENG",
  "token_env": "TRIAGELAB_LINEAR_TOKEN",
  "poll_seconds": 60,
  "investigation_comments": true,
  "policy": {
    "tag": "triagelab_python_timeout",
    "auto_questions": true,
    "max_questions": 2
  }
}
```

Create a personal API key in **Settings → Security & access → API**. The key
acts as your Linear account, so use a dedicated service account that can read
the team and post comments. `token_env` names the environment variable
holding the key — the key itself never appears in configuration files, the
browser, the investigator, or reports. Linear has no anonymous access, so a
configured but missing or malformed key stops startup. For read-only
operation set `auto_questions: false`, `max_questions: 0`, and
`investigation_comments: false`; the key is still required. Linear has no
private issue comments, so `internal_notes` must stay false, and the Zendesk
priority and custom-field policy settings do not apply.

Start the workspace on the VM:

```sh
bin/triagelab serve --tickets linear.local.json --output reports/linear
```

Use an SSH tunnel to open `http://127.0.0.1:8080/tickets`. One workspace has
one source, workspace, and team; use a separate report directory per team and
preserve its private `.tickets` directory across restarts.

Admins can also add Linear connections in the workspace UI on the
**Connections** page, pasting the API key or naming the environment variable
that holds it.

## Prepare evidence and launch

**Edit procedures** in Ticket cases sets up a procedure per label with its own
evidence requirements, clarification policy, agent, and sandbox; the first
matching procedure applies. Preview an issue by its team number (for example
`12` for `ENG-12`) — preview reads without enrolling; **Track issue** keeps
its preparation state.

The procedure takes configured facts and files from the issue author's
original description and subsequent comments; other participants' replies do
not supply evidence. For the starter checklist the author provides
`Python version:`, `Expected:`, and `Actual:` lines. At most four sanitized
files (`.py`, `.json`, `.txt`, `.log`) of 1 MiB each enter a run; review
source references and hashes before approval.

With `auto_questions: true`, review the author, label policy, and exact draft
before **Approve and send clarification**; comments are visible to everyone
with workspace access. Follow-ups are authorized only to the same author
under the reviewed policy; `max_questions` (1–3) caps send attempts,
identical questions are not repeated, and an unanswered clarification routes
the issue for review after seven days. Every send is reserved in durable case
state first; a confirmed comment is identified by ID and never resent, while
an uncertain delivery stays held for manual inspection.

A procedure can start automatically when the evidence is ready.
[Automatic ticket investigations](/TriageLab/guides/workspace/#automatic-ticket-investigations).

For the default review path, **Approve and start investigation** binds approval to
the selected evidence and run settings, expires after 15 minutes, and rereads
the issue before launch. Each tracked issue consumes one investigation
attempt and shares the workspace's single run slot.

## Share an investigation summary

With `investigation_comments: true`, choose **Review comment** on a completed
report. The preview shows the run ID, outcome, a bounded agent summary, and
selected process IDs, commands, and exit codes, wrapped so imported text
cannot create mentions or links. A confirmed send stores its comment ID; an
uncertain delivery keeps its reservation and blocks another send for the same
investigation.

## Limits

One team per workspace instance; inbox synchronization is unsupported. More
than a bounded number of issue comments, oversized responses, or inaccessible
evidence require manual review; Linear API rate limits can temporarily
prevent refresh. Imported prose and files remain untrusted — review evidence
for secrets before sending it to a model. The connector is fixture-verified;
a controlled live Linear pilot remains to be run.
