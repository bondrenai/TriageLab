# Prepare Zendesk tickets for investigation

The first ticket procedure handles a small, dependency-free Python 3.12 timeout
reproduction. It checks what the requester has already provided, drafts or
sends a clarification under policy, and hands approved evidence to the existing
KAgent investigator. The helpdesk remains the ticket and conversation record.
TriageLab keeps preparation decisions and the linked investigation report.

## Connect a test account

1. Build the Go binary and browser assets as described in [UI setup](ui.md).
2. Copy `tickets.example.json` to `tickets.local.json`, then set the Zendesk
   subdomain and agent email. Supply the Zendesk API token through
   `TRIAGELAB_ZENDESK_TOKEN` in the server environment. This uses Zendesk's
   API token authentication; the configured agent must be able to read the
   selected tickets and, if sending is enabled, add public replies.
3. Start the workspace with
   `bin/triagelab serve --tickets tickets.local.json`. Keep its listener on
   loopback and use the usual SSH tunnel for a remote VM. Configure KAgent
   separately before approving a lab.
4. Open **Ticket cases** and preview a ticket by numeric ID. Preview does not
   enroll the ticket, send a message, or start a run. Track a matching ticket
   to start applying the procedure.

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
`.log` files together on one public requester message. For a disposable ticket,
the files in [the timeout fixture](../examples/config-bug/) provide a small
example. The latest requester message with a Python attachment defines the
file set. Only four files of up to 1 MiB each are accepted. Attachment downloads
must stay on the configured Zendesk origin and cannot redirect. A ticket with
more than 100 comments, an unsupported Python version or attachment, or unsafe
evidence requires review.

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

This is a private, single operator pilot. Zendesk plans to retire API token
authentication, so wider rollout needs OAuth. See [Zendesk authentication
requirements](https://developer.zendesk.com/api-reference/introduction/security-and-auth/).
A ticket is enrolled explicitly;
no general Zendesk inbox sync or public webhook listener is needed. Use one
workspace per account for automatic replies. Its `reports/.tickets` directory
stores up to 100 tracked cases, including redacted facts, approval state,
clarification reservations and run references. Keep it private and preserve it
across restarts: deleting it loses the duplicate-send and run limits. Account
changes require a separate report directory. The API token is held server-side,
and the investigator receives only the selected evidence. Review the
[security model](../SECURITY.md) before sending customer data to a model.

The connector boundary supports adding Bondren next. For imported tickets,
that connector must preserve original Zendesk identity and agree which
application owns progression and customer replies. Bondren workflows can then
use their existing condition and approval steps to drive the preparation and
run operations. CEL and visual authoring can follow once more procedures
demonstrate a need for them.
