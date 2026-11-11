---
title: Zendesk tickets
description: Prepare Zendesk tickets for investigation with policy-checked clarifications and configurable launch.
---

The Zendesk source handles a small, dependency-free Python 3.12 timeout
reproduction. It checks what the requester has already provided, drafts or
sends a clarification under policy, and prepares evidence for the
existing kagent investigator. Zendesk remains the ticket and conversation
record; TriageLab keeps preparation decisions and the linked investigation
report.

## Connect a test account

1. In Zendesk Admin Center, create an OAuth **Public client** named
   **TriageLab** with identifier `triagelab-local` and redirect URL
   `http://127.0.0.1:8080/oauth/zendesk/callback`. Allow `tickets:read` and
   `ticket_attachments:read`; also allow `tickets:write` for public
   clarifications or internal notes.
2. Copy `tickets.example.json` to `tickets.local.json` and set the subdomain,
   client identifier, and exact registered redirect URI.
3. Start `bin/triagelab serve --tickets tickets.local.json` on loopback (SSH
   forward for a remote VM) and open `http://127.0.0.1:8080/tickets`.
4. Choose **Connect Zendesk** and approve access as an agent who can read the
   selected tickets. TriageLab uses an authorization code with PKCE and only
   requests the scopes your policy needs.
5. Preview a ticket by numeric ID. Preview does not enroll the ticket, send a
   message, or start a run. **Track** a matching ticket to begin the procedure.

Configure the source and initial policy in the JSON file (see
[configuration examples](/TriageLab/reference/configuration-examples/); use
**Ticket cases → Edit procedures** to add or edit procedures at runtime. Each
procedure has its own match tag, evidence checklist, clarification settings,
agent, and sandbox; the first matching procedure applies. An open ticket must
carry the procedure's tag, match its priorities if provided, and match an
optional custom-field condition.

## Clarify and launch

With `auto_questions: false`, review the draft and reply in Zendesk yourself.
With `auto_questions: true`, track the ticket and review the requester,
matching rule, and exact draft before **Approve and send clarification**. A
change to the requester or policy requires fresh approval. Subsequent
refreshes can send narrower follow-ups while the newest public message is from
the requester; `max_questions` (1–3) caps send attempts per ticket, identical
questions are not repeated, and conflicts detected by Zendesk safe updates are
shown rather than overwritten. Uncertain results are held for manual
inspection and never automatically resent.

Every clarification draft passes an output sanitizer before it can be sent: a
draft that may disclose a configured credential, another tracked requester's
identity, or internal-note text is held for review with the reason shown —
never sent or rewritten automatically. Facts from public requester messages
are sanitized into evidence the same way (credential-looking assignments are
redacted and the applied redaction classes are recorded), while a fact that
contains one of the configured credentials holds the whole case.

For the starter checklist, the requester supplies lines like:

```text
Python version: 3.12.4
Expected: The health check allows five seconds for a response.
Actual: The deadline expires after five milliseconds.
```

Attach only the sanitized files the procedure needs (`.py`, `.json`, `.txt`,
`.log`; append `.txt` if Zendesk rejects an extension). At most four files of
1 MiB each enter a run, and the report records the source of each.

A procedure can start automatically when the evidence is ready.
[Automatic ticket investigations](/TriageLab/guides/workspace/#automatic-ticket-investigations).

For the default review path, check the source references, file hashes, and run
limits, then choose **Approve and start investigation**. Approval expires
after 15 minutes and is rechecked against current ticket access, evidence,
procedure, and run settings — a stale plan cannot launch work. Each tracked
case consumes one investigation attempt and shares the workspace's single run
slot.

## Share results internally

Set `internal_notes: true` (reconnect Zendesk with `tickets:write` if the
grant was read-only). From a completed investigation, **Review internal note**
shows the run ID, outcome, a bounded agent summary, and selected process IDs,
commands, and exit codes — the same sanitizer checks the preview, and a note
that may leak a credential or another requester's identity is blocked from
posting. TriageLab records the Zendesk comment ID and blocks a duplicate send
for the same investigation, including across restarts.

## Boundaries

This is a private, single-operator pilot. Its `reports/.tickets` directory
stores up to 100 tracked cases with approval state, reservations, receipts,
and run references — preserve it across restarts. The OAuth grant is stored
server-side (mode 0600) and renewed automatically; changing the client,
redirect URI, or scope requires reconnecting. Use one workspace per account,
and review the security model before sending customer data to a model.
