---
title: GitHub issues
description: Prepare issues from one GitHub.com repository with reviewed clarifications, summaries, and approval before a run.
---

TriageLab reads issues from one GitHub.com repository and prepares them for
investigation. Each issue goes through the first matching procedure: the
built-in Python 3.12 timeout procedure, or one an admin adds. The workspace
provides preview, tracking, reviewed clarification comments, evidence review,
and approval before a sandbox run; completed investigations can publish a
reviewed summary to the linked issue. GitHub remains the issue and
conversation record.

## Configure a repository

Copy `github.example.json` to `github.local.json`, set `repository` to
`owner/repository`, and set `policy.tag` to the label that selects open
issues for the procedure:

```json
{
  "source": "github",
  "repository": "your-owner/your-repository",
  "token_env": "TRIAGELAB_GITHUB_TOKEN",
  "poll_seconds": 60,
  "investigation_comments": true,
  "policy": {
    "tag": "triagelab_python_timeout",
    "auto_questions": true,
    "max_questions": 2
  }
}
```

Create a fine-grained token limited to the repository with **Issues: read and
write**. The `token_env` setting names the environment variable holding it —
the token itself never appears in configuration files, the browser, the
investigator, or reports. A configured but missing or malformed token stops
startup. For read-only operation, set `auto_questions: false`,
`max_questions: 0`, and `investigation_comments: false`; public issues can
then be read anonymously by omitting `token_env`. GitHub has no private issue
notes, so the Zendesk `internal_notes` setting must stay false.

Start the workspace on the VM:

```sh
bin/triagelab serve --tickets github.local.json --output reports/github
```

Use an SSH tunnel to open `http://127.0.0.1:8080/tickets`. One workspace has
one source and one repository; use a separate report directory per
repository and preserve its private `.tickets` directory across restarts.

## Prepare and approve evidence

**Edit procedures** in Ticket cases sets up a procedure per label with its own
evidence requirements, clarification policy, agent, and sandbox; the first
matching procedure applies. Preview an issue by number — preview reads without
enrolling; **Track issue** keeps its preparation state. An open issue must
carry the configured label.

The procedure takes configured facts and files from the issue author's
original body and subsequent comments; other participants' replies do not
supply evidence. For the starter checklist the author provides
`Python version:`, `Expected:`, and `Actual:` lines. Supported files are
`.py`, `.json`, `.txt`, and `.log` uploaded as standard GitHub Markdown
attachment links (append `.txt` if GitHub rejects an extension — TriageLab
restores it). At most four files of 1 MiB each enter a run; review source
references and SHA-256 hashes before approval.

With `auto_questions: true`, review the author, label policy, and exact draft
before **Approve and send clarification**; comments are visible to everyone
with repository access and public in public repositories. Follow-ups are
authorized only to the same author under the reviewed policy; `max_questions`
(1–3) caps send attempts, identical questions are not repeated, and an
unanswered clarification routes the issue for review after seven days.
Every send is reserved in durable case state first; confirmed comments are
identified by comment ID and never resent, while an uncertain delivery stays
held for manual inspection. GitHub has no conditional writes for comments, so
TriageLab rechecks the issue before posting but cannot guarantee an atomic
compare-and-send.

Once evidence is ready, **Approve and start investigation** binds approval to
the selected evidence and run settings, expires after 15 minutes, and
rereads the issue before launch. Each tracked issue consumes one
investigation attempt and shares the workspace's single run slot.

## Share an investigation summary

With `investigation_comments: true`, choose **Review comment** on a completed
report. The preview shows the run ID, outcome, a bounded agent summary, and
selected process IDs, commands, and exit codes; imported text is wrapped in a
literal code block so it cannot create mentions or links. The summary is an
ordinary issue comment — not a private note. A confirmed send stores its
comment ID; an uncertain delivery keeps its reservation and blocks another
send for the same investigation.

## Limits

GitHub.com issues only: pull requests, Enterprise hosts, inbox
synchronization, and repository cloning are unsupported. Attachments must be
publicly downloadable GitHub uploads. More than 100 issue comments,
oversized responses, or inaccessible evidence require manual review; API rate
limits can temporarily prevent refresh. Imported prose and files remain
untrusted — review evidence for secrets before sending it to a model. The
connector is fixture-verified; no live GitHub clarification or summary sends
were performed while implementing it.
