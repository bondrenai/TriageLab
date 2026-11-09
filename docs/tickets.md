# Prepare Zendesk tickets for investigation

The first ticket procedure handles a small, dependency-free Python 3.12 timeout
reproduction. It checks what the requester has already provided, drafts or
sends a clarification under policy, and hands approved evidence to the existing
KAgent investigator. The helpdesk remains the ticket and conversation record.
TriageLab keeps preparation decisions and the linked investigation report.

## Connect a test account

1. In Zendesk Admin Center, open **Apps and integrations → APIs → OAuth clients**.
   Create a [Public client](https://developer.zendesk.com/documentation/authentication/oauth-migration/#setting-up-an-oauth-client)
   named **TriageLab**. Use the identifier `triagelab-local` and redirect URL
   `http://127.0.0.1:8080/oauth/zendesk/callback`. Allow `tickets:read` for
   ticket details and comments, and `ticket_attachments:read` for downloading
   evidence files. Also allow `tickets:write` if you plan to enable automatic
   public clarifications.
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
   read the selected tickets and, when enabled, post public replies.
   TriageLab uses an authorization code with PKCE and requests
   `tickets:read` and `ticket_attachments:read`. It adds `tickets:write`
   when the policy enables automatic questions. The browser handles consent.
5. Preview a ticket by numeric ID. Preview does not enroll the ticket, send
   a message, or start a run. Track a matching ticket to start applying the
   procedure. Configure KAgent separately before approving a lab.

Configure one procedure in the JSON file and restart the workspace to apply
changes. Conditions are combined: an open ticket must have the configured
`tag`, match one of `priorities` if provided, and match the optional string
`field_value` for Zendesk custom `field_id`. For dropdowns use the stored option
value. This slice uses those typed conditions and a fixed sequence of actions.

## Clarify and approve

With `auto_questions: false`, review the draft and reply in Zendesk. With
`auto_questions: true`, **Track and send question** can post the previewed
public clarification if the ticket is still current. Subsequent refreshes
can send narrower questions under the same policy. Automatic sending requires
the newest public message to be from the requester. The configured
`max_questions` (1–3 when enabled) limits send attempts per ticket. An identical
question is not repeated, and a reply with no progress is routed for review.
A change detected by Zendesk's safe update is shown as a conflict; a timeout or
other uncertain result is held for manual inspection and never automatically
resent. See [Zendesk safe updates](https://developer.zendesk.com/documentation/ticketing/managing-tickets/creating-and-updating-tickets/#protecting-against-ticket-update-collisions).

The procedure uses facts from public requester messages. The requester can
supply these lines across replies:

```text
Python version: 3.12.4
Expected: The health check allows five seconds for a response.
Actual: The deadline expires after five milliseconds.
```

Attach a sanitized `.py` reproduction and any required `.json`, `.txt`, or
`.log` files in public requester replies. If Zendesk rejects a
script or config extension, append `.txt` to the filename, as in `app.py.txt`
or `config.json.txt`. TriageLab removes that suffix for the sandbox and rejects
files that resolve to the same name. For a disposable ticket,
the files in [the timeout fixture](../examples/config-bug/) provide a small
example. The latest requester reply with a Python attachment defines the
file set. If it has no JSON config, TriageLab adds the JSON files from the
latest public requester reply that has JSON files and no Python attachment.
The report records both source comments. Only four files of up to 1 MiB each
are accepted. Attachment requests
start on the configured account. TriageLab accepts one temporary HTTPS redirect
to a numbered Zendesk attachment host and fetches it without the OAuth token.
A ticket with more than 100 comments, an unsupported Python version or
attachment, or unsafe evidence requires review.

The server checks one waiting ticket each configured polling interval
(60–3600 seconds, round robin). Set `poll_seconds: 0` to refresh manually.
In draft mode, use **Refresh ticket** after a reply. **Pause tracking** stops
automatic work for a case. An unanswered clarification is escalated for review
after seven days when checked. Waiting and drafting never allocate a sandbox
or call a model.

Once evidence is ready, review its source references, file hashes, and run
limits, then choose **Approve and start investigation**. Approval expires after
15 minutes. It is checked against current ticket access, evidence, procedure,
and run settings. A stale plan cannot launch work. Each tracked case can
consume one investigation attempt, and the workspace shares its single run
slot between ticket cases and manual investigations. Open the linked report to
inspect and share findings through the normal helpdesk process.

## Deployment boundary

This is a private, single operator pilot with an OAuth client registered in
the connected account. A managed integration distributed across Zendesk accounts
needs [Zendesk global OAuth](https://developer.zendesk.com/api-reference/introduction/security-and-auth/).
A ticket is enrolled explicitly;
no general Zendesk inbox sync or public webhook listener is needed. Use one
workspace per account for automatic replies. Its `reports/.tickets` directory
stores up to 100 tracked cases, including redacted facts, approval state,
clarification reservations and run references. Keep it private and preserve it
across restarts: deleting it loses the duplicate-send and run limits. Account
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
