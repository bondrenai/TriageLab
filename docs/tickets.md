# Prepare Zendesk tickets for investigation

The first ticket procedure handles a small, dependency-free Python 3.12 timeout
reproduction. It checks what the requester has already provided, drafts or
sends a clarification under policy, and prepares evidence for the existing
kagent investigator. The helpdesk remains the ticket and conversation record.
TriageLab keeps preparation decisions and the linked investigation report.

## Connect a test account

1. In Zendesk Admin Center, open **Apps and integrations → APIs → OAuth clients**.
   Create a [Public client](https://developer.zendesk.com/documentation/authentication/oauth-migration/#setting-up-an-oauth-client)
   named **TriageLab**. Use the identifier `triagelab-local` and redirect URL
   `http://127.0.0.1:8080/oauth/zendesk/callback`. Allow `tickets:read` for
   ticket details and comments, and `ticket_attachments:read` for downloading
   evidence files. Also allow `tickets:write` for public clarifications or
   internal notes.
2. Build the Go binary and browser assets as described in [UI setup](ui.md).
   Copy `tickets.example.json` to `tickets.local.json`. Set the Zendesk
   subdomain, client identifier, and exact registered redirect URI. Existing
   pilot configurations should replace the old `email` and `token_env`
   settings with `client_id` and `redirect_uri`.
3. Start `bin/triagelab serve --tickets tickets.local.json`. Keep its listener
   on loopback. For a remote VM, forward local port 8080 to VM loopback port
   8080 over SSH. Open `http://127.0.0.1:8080/tickets` in the browser, using
   the same hostname and port registered for OAuth.
4. Choose **Connect Zendesk** and approve access as a Zendesk agent who can
   read the selected tickets and, when enabled, post replies or internal notes.
   TriageLab uses an authorization code with PKCE and requests
   `tickets:read` and `ticket_attachments:read`. It adds `tickets:write`
   when `auto_questions` or `internal_notes` is enabled. The browser handles
   consent.
5. Preview a ticket by numeric ID. Preview does not enroll the ticket, send
   a message, or start a run. Track a matching ticket to start applying the
   procedure. Configure kagent separately before launching an investigation.

Configure the source and initial policy in the JSON file. Use **Ticket cases →
Edit procedures** to add or edit procedures and save them while the workspace
is running. Each procedure has its own match tag, evidence, clarification,
agent, and sandbox. TriageLab uses the first procedure that fits, so place
specific product tags before broader rules. The original procedure starts last
when you add new ones and still has to match. An open ticket must have the
procedure's `tag`, match one of its `priorities` if provided, and match the optional string
`field_value` for Zendesk custom `field_id`. For dropdowns use the stored option
value. This slice uses those typed conditions and a fixed sequence of actions.

The editor starts with a removable Python timeout checklist. Add, edit, or
remove text fields and attachment rules. Text fields match `Label: value`
lines and can validate the value with a regular expression. Attachments match
a filename or a supported extension. Set which items are required for investigation,
write the question for each required item, and optionally add an opening message.
Only missing required items generate questions. An empty checklist lets a
matching ticket proceed without collecting fields or attachments.
**Preview draft on ticket** checks the selected procedure on its own.
The main ticket preview uses the saved order and shows the matched procedure.
Preview does not track the ticket, send a question, or start a run.

Set a default agent and a sandbox template. Add routing rules to
select agents by exact tag. If matching tags select different agents, track the
case and choose one of the matching agents. The proposed run displays the agent,
sandbox, and matching tags. Changes to the procedure, route, or run target
invalidate the previous plan. An unavailable target holds the case for review.
Saved agents and alternative sandbox templates use the workspace's kagent
deployment connection. Allow `get` and `list` on `sandboxtemplates` in its
namespace Role. The source's startup policy sets the maximum clarification
permission available to the editor.

## Clarify and approve

With `auto_questions: false`, review the draft and reply in Zendesk. With
`auto_questions: true`, track the ticket and review the requester, matching
rule, and exact draft. If Zendesk omits the email address, open the requester
link to verify it. **Approve and send clarification** sends the public reply
and authorizes follow-ups to the reviewed requester under that policy.
A change to the requester or policy requires fresh approval. Subsequent
refreshes can send narrower questions if the newest public message is from
the requester. Zendesk applies the account's notification rules. The configured
`max_questions` (1–3 when enabled) limits send attempts per ticket. An identical
question is not repeated, and a reply with no progress is routed for review.
A change detected by Zendesk's safe update is shown as a conflict; a timeout or
other uncertain result is held for manual inspection and never automatically
resent. See [Zendesk safe updates](https://developer.zendesk.com/documentation/ticketing/managing-tickets/creating-and-updating-tickets/#protecting-against-ticket-update-collisions).

Every clarification draft passes an output sanitizer before it can be sent. A
draft that may disclose a configured credential, another tracked requester's
identity, or text from this ticket's internal notes is held for review with the
reason shown on the case; it is never sent or rewritten automatically. Matching
is best effort and deliberately errs toward holding, so a harmless draft can be
held when names read alike.

The procedure uses facts from public requester messages. Fact text is sanitized
into evidence: credential-looking assignments and token shapes are redacted —
the applied redaction classes are recorded in the run's case source — while a
fact containing one of the configured credentials holds the whole case for
review at the source. With the starter
checklist, the requester can supply these lines across replies:

```text
Python version: 3.12.4
Expected: The health check allows five seconds for a response.
Actual: The deadline expires after five milliseconds.
```

Attach only the sanitized files needed for the procedure in public requester
replies. Supported types are `.py`, `.json`, `.txt`, and `.log`. If Zendesk rejects a
script or config extension, append `.txt` to the filename, as in `app.py.txt`
or `config.json.txt`. TriageLab removes that suffix for the sandbox and rejects
files that resolve to the same name. For a disposable ticket,
the files in [the timeout fixture](../examples/config-bug/) provide a small
example. The first matching required attachment rule, or the first matching
optional rule, selects the latest requester reply with that attachment. Other
rules can pick up missing attachments from separate replies. The report
records the source of each file. Only four files of up to 1 MiB each
are accepted. Attachment requests
start on the configured account. TriageLab accepts one temporary HTTPS redirect
to a numbered Zendesk attachment host and fetches it without the OAuth token.
A ticket with more than 100 comments, unsupported attachments, or unsafe
evidence requires review.

The server checks one eligible tracked ticket each configured polling interval
(60–3600 seconds, round robin). Set `poll_seconds: 0` to refresh manually.
In draft mode, use **Refresh ticket** after a reply. **Pause tracking** stops
automatic work for a case. An unanswered clarification is escalated for review
after seven days when checked. Waiting and drafting never allocate a sandbox
or call a model.

A procedure can start automatically when the evidence is ready. Set
`policy.auto_run_condition` to a boolean CEL expression over ticket attributes
to restrict automatic launches, such as `"reproduce" in ticket.tags`.
[Automatic ticket investigations](https://bondrenai.github.io/TriageLab/guides/workspace/#automatic-ticket-investigations).

For the default review path, check the source references, file hashes, and run
limits, then choose **Approve and start investigation**. Approval expires after
15 minutes. It is checked against current ticket access, evidence, procedure,
and run settings. A stale plan cannot launch work. Each tracked case can
consume one investigation attempt, and the workspace shares its single run
slot between ticket cases and manual investigations.

## Share the investigation in Zendesk

Set `internal_notes: true` in the ticket configuration. If the workspace had
read-only access, reconnect Zendesk to grant `tickets:write`. From a completed
ticket investigation, choose **Review internal note**. The preview contains
the run ID, outcome, a bounded agent summary, and selected process IDs,
commands, and exit codes. The same output sanitizer checks the previewed body:
a note that may disclose a configured credential or another tracked requester's
identity is shown as blocked with its findings and cannot be posted from
TriageLab. Check the preview before choosing **Post internal
note**. Zendesk receives a private comment on the linked ticket.

TriageLab records the Zendesk comment ID on confirmation. A repeated approval
returns the existing receipt. If delivery is uncertain, use the run ID to
inspect the Zendesk conversation. The workspace keeps that reservation across
restarts and blocks another send for the same investigation.

## Deployment boundary

This is a private, single operator pilot with an OAuth client registered in
the connected account. A managed integration distributed across Zendesk accounts
needs [Zendesk global OAuth](https://developer.zendesk.com/api-reference/introduction/security-and-auth/).
A ticket is enrolled explicitly;
no general Zendesk inbox sync or public webhook listener is needed. Use one
workspace per account for automatic replies. Its `reports/.tickets` directory
stores up to 100 tracked cases, including redacted facts, approval state,
clarification and note reservations, delivery receipts, and run references.
Preserve that private directory across restarts to retain the duplicate-send
and run limits. Account
changes require a separate report directory. The OAuth grant is stored
server-side in `reports/.tickets/.zendesk-oauth` (mode 0600). TriageLab
[renews expired access](https://developer.zendesk.com/documentation/authentication/refresh-token/)
by replacing both tokens. Changing the client, redirect URI, or scope requires
reconnecting. If renewal fails, reconnect through Ticket cases. Revoke unwanted
grants in Zendesk. The investigator receives only selected evidence; review the
[security model](../SECURITY.md) before sending customer data to a model.

The connector boundary supports adding Bondren next. For imported tickets,
that connector must preserve original Zendesk identity and agree which
application owns progression and customer replies. Bondren workflows can then
use their existing condition and approval steps to drive the preparation and
run operations. CEL and visual authoring can follow once more procedures
demonstrate a need for them.
