---
title: GitLab issues
description: Prepare issues from one GitLab.com or self-managed project with reviewed clarifications and comments.
---

TriageLab reads issues from one GitLab project — on GitLab.com or a
self-managed instance — and prepares them for investigation. Each issue
goes through the first matching procedure: the built-in Python 3.12
timeout procedure, or one an admin adds. The workspace provides preview,
tracking, reviewed clarification notes, and an evidence checklist.
Completed investigations can publish a reviewed comment to the linked
issue. GitLab remains the issue and conversation record.

GitLab issues are looked up by their per-project number (the iid) or a
pasted issue URL; both map to the numeric iid, which stays the case key.
The connector uses the documented GitLab REST v4 API as reviewed on
2026-11-11: issue reads, issue notes with system and internal notes
excluded from evidence, note writes validated against GitLab's documented
body normalization, and attachment downloads through the project Markdown
uploads endpoint (GitLab 17.4 and newer).

## Configure a project

Copy `gitlab.example.json` to `gitlab.local.json`, set `repository` to
the project path (`group/project`, including subgroups), set `origin` to
your instance for self-managed GitLab (omit it for GitLab.com), and set
`policy.tag` to the GitLab label that selects open issues:

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
`write_api`). `token_env` names the environment variable holding the
token — the token itself never appears in configuration files, the
browser, the investigator, or reports — and is sent as the
`PRIVATE-TOKEN` header. A configured but missing token stops startup; the
token is required, and anonymous reads are not supported. For read-only
operation set `auto_questions: false` and `investigation_comments:
false`. Investigation summaries post as ordinary notes — internal notes
are not offered for GitLab — and the Zendesk priority and custom-field
policy settings do not apply.

Start the workspace on the VM:

```sh
bin/triagelab serve --tickets gitlab.local.json --output reports/gitlab
```

Use an SSH tunnel to open `http://127.0.0.1:8080/tickets`. One connection
watches one project; use a separate report directory per project and
preserve its private `.tickets` directory across restarts.

Admins can also add GitLab connections in the workspace UI on the
**Connections** page, pasting the access token or naming the environment
variable that holds it.

## Prepare evidence and launch

**Edit procedures** in Ticket cases sets up a procedure per label with
its own evidence requirements, clarification policy, agent, and sandbox;
the first matching procedure applies. Preview an issue by its number or a
pasted issue URL — the case reference is always the numeric issue number
once resolved. Preview reads without enrolling; **Track issue** keeps its
preparation state. Only issues in the `opened` state are open for
investigation; closed issues, incidents, tasks, and other work item types
are excluded.

The procedure takes configured facts and files from the requester's
visible notes and the issue description; system notes and internal notes
never supply evidence. For the starter checklist the requester provides
`Python version:`, `Expected:`, and `Actual:` lines. Descriptions and
note bodies are Markdown and stay inert imported data. At most four
sanitized files (`.py`, `.json`, `.txt`, `.log`) of 1 MiB each enter a
run; Markdown uploads download through the authorized project uploads
endpoint on the configured origin, which fails closed on an unexpected
shape and requires GitLab 17.4 or newer. Review source references and
hashes before approval.

With `auto_questions: true`, review the requester, label policy, and
exact draft before **Approve and send clarification**; the note is
visible to everyone with access to the issue and its project, including
the requester on confidential issues. Follow-ups are authorized only to
the same requester under the reviewed policy; `max_questions` (1–3) caps
send attempts, identical questions are not repeated, and an unanswered
clarification routes the issue for review after seven days. Notes posted
by the configured access token carry its account and never count as
requester replies unless the same account authored the issue. Every send
is reserved in durable case state first; a confirmed note is identified
by ID and never resent, while an uncertain delivery stays held for
manual inspection.

A procedure can start automatically when the evidence is ready.
[Automatic ticket investigations](/TriageLab/guides/workspace/#automatic-ticket-investigations).

For the default review path, **Approve and start investigation** binds
approval to the selected evidence and run settings, expires after 15
minutes, and rereads the issue before launch. Each tracked issue consumes
one investigation attempt and shares the workspace's single run slot.

## Share an investigation summary

With `investigation_comments: true`, choose **Review investigation
comment** on a completed report. The preview shows the run ID, outcome, a
bounded agent summary, and selected process IDs, commands, and exit
codes; the comment posts as a fenced plain-text note visible to everyone
with access to the issue. A confirmed send stores its note ID; an
uncertain delivery keeps its reservation and blocks another send for the
same investigation.

## Limits

One project per connection, on GitLab.com or a self-managed instance
reached over HTTPS. Cases are keyed by the per-project issue number.
GitLab priority and custom-field policy, issue transitions, saved issue
lists, epics, merge requests, other work item types, and internal notes
are unsupported. An issue's total note count — including system notes —
is capped at 100; more than that, oversized responses, or inaccessible
evidence require manual review; GitLab API rate limits can temporarily
prevent refresh. The uploads download endpoint requires GitLab 17.4 or
newer. Imported prose and files remain untrusted — review evidence for
secrets before sending it to a model. Connector fixtures exercise
parsing, delivery, and security boundaries, and live checks against a
disposable self-managed GitLab CE 19.5.5 instance passed on 2026-11-12
(connection verification, upload download through real evidence
preparation, clarification with a bounded follow-up, and
investigation-comment delivery); GitLab.com and a model-backed run
approved from a GitLab case remain to be exercised.
