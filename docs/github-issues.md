# Prepare GitHub issues for investigation

TriageLab can read issues from one GitHub.com repository and prepare them for
investigation. Each issue goes through the first matching procedure: the
built-in Python 3.12 timeout procedure, or one an admin adds. The workspace
provides preview, tracking, reviewed clarification comments, and an evidence
checklist. Completed investigations can also publish a reviewed summary to the
linked issue. GitHub remains the issue and conversation record.

## Configure a repository

Build the CLI and browser assets on your development VM as described in
[UI setup](ui.md). Copy [github.example.json](../github.example.json) to
`github.local.json`, set `repository` to `owner/repository`, and set `policy.tag`
to the GitHub label that selects open issues for the built-in procedure.

The example enables clarification comments and investigation summaries. Create
a fine-grained token limited to the selected repository with **Issues: read and
write** permission. Reading uses the
[issue](https://docs.github.com/en/rest/issues/issues?apiVersion=2026-03-10#get-an-issue)
and [comment](https://docs.github.com/en/rest/issues/comments?apiVersion=2026-03-10#list-issue-comments)
endpoints; posting requires
[Issues: write](https://docs.github.com/en/rest/issues/comments?apiVersion=2026-03-10#create-an-issue-comment).
Set `TRIAGELAB_GITHUB_TOKEN` on the server through your secret manager or a hidden
shell prompt before starting TriageLab. The `token_env` setting names this
environment variable; it never contains the token itself. Keep the token out of
configuration files and issue evidence. It stays server-side and is excluded
from the browser, investigator, and reports. GitHub uses this configured token;
there is no GitHub OAuth connection flow.
A configured but missing or malformed token stops startup; GitHub credential
or permission errors are reported when accessing an issue or posting a comment.

For read-only operation, set `auto_questions: false`, `max_questions: 0`, and
`investigation_comments: false`. Public issues can then be read anonymously by
omitting `token_env`. Private issue text still requires a token with Issues read
access. `internal_notes` must stay false or be omitted for GitHub: GitHub has no
private issue notes, and enabling that Zendesk setting is rejected.

Start the workspace on the VM:

```sh
bin/triagelab serve --tickets github.local.json --output reports/github
```

Use an SSH tunnel to open `http://127.0.0.1:8080/tickets`; keep the server on
loopback. Configure kagent and its invocation credential before starting
investigations. Preview only reads the issue. Tracking a ready issue can
start a run when the procedure enables automatic launch.

One workspace has one source and one repository. Use a separate report directory
for each repository and for Zendesk. Preserve its private `.tickets` directory
across restarts: it stores tracked cases, approval state, clarification and
summary delivery reservations and receipts, and consumed run attempts. Changing
the repository requires a separate directory. Restart the server after changing
configuration or replacing its environment token.

Connections can also be created in the workspace UI. On the **Connections**
page an admin adds a GitHub connection and pastes the token there; the server
stores it in the private connection registry instead of an environment
variable. Anonymous read remains available without any token.

## Prepare evidence

**Edit procedures** in Ticket cases lets you set up a procedure for each
product label with its own evidence requirements, clarification, agent, and
sandbox. The first matching procedure applies. Use **Preview draft on issue**
to check an individual procedure before saving. See
[procedure editing](tickets.md#clarify-and-approve).

Enter the issue number from the configured repository and choose **Preview
issue**. Preview reads the issue without enrolling it. Choose **Track issue**
to keep its preparation state. An open issue must carry the configured label.

The procedure takes configured facts and files from the issue author's original
body and subsequent comments. Other participants' replies do not supply
evidence. For the starter Python timeout checklist, provide these fields as
individual lines:

```text
Python version: 3.12.4
Expected: The health check allows five seconds for a response.
Actual: The deadline expires after five milliseconds.
```

Upload sanitized files that meet the procedure's attachment rules
to GitHub, keeping the standard Markdown attachment links in the author's body
or comment. Supported files are `.py`, `.json`, `.txt`, and `.log`. If GitHub
rejects a script or configuration extension, append `.txt`, such as
`app.py.txt` or `config.json.txt`; TriageLab restores the original extension.
Names must be unique after this normalization. At most four files, each no
larger than 1 MiB, can enter a run.

The first matching required attachment rule, or the first matching optional
rule, selects the latest author message with that attachment. Other attachment
rules can fill gaps from separate messages. Review the source references and
SHA-256 hashes before approval.

## Clarify and approve

With `auto_questions: true`, track the issue and review its author, matching
label policy, and exact draft. **Approve and send clarification** posts that
comment and authorizes narrower follow-ups to the same author under the reviewed
policy. Comments are visible to everyone with access to the repository; in a
public repository they are public. GitHub applies its
[comment notification rules](https://docs.github.com/en/rest/issues/comments?apiVersion=2026-03-10#create-an-issue-comment).
A change to the author or policy requires fresh approval.

Subsequent refreshes can send a follow-up when the newest conversation message
is a reply from the author. `max_questions` must be 1–3 when automatic questions
are enabled and caps total send attempts per issue. Identical questions are not
repeated; a reply with no progress requires review. Confirmed outgoing comments
are identified by their GitHub comment IDs and never count as author replies,
including when the issue author and the account behind the token are the same.
If a clarification remains unanswered for seven days, the next check routes the
issue for review.

Set `poll_seconds` to 60–3600 to check one eligible tracked issue per interval, in turn.
Set it to `0` to refresh manually. **Refresh and apply policy** can also check a
tracked issue immediately; **Pause tracking** stops its automatic work. Tracking
does not enumerate a repository inbox. With `auto_questions: false`, review the
draft, post it manually in GitHub, and choose **Refresh issue** after the author
replies. Waiting and drafting do not allocate a sandbox or call a model.

Every send is reserved in durable case state first. A confirmed receipt records
the GitHub comment ID; confirmed comments are not resent. A timeout, unexpected
response, or missing receipt leaves delivery unconfirmed across restarts. Inspect
the issue in GitHub; TriageLab does not automatically retry an uncertain send.
Before sending, the connector rechecks the issue and comments. GitHub
[does not support conditional writes for this endpoint](https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api#use-conditional-requests),
so another edit can still occur between that check and the POST. This is not an
atomic compare-and-send guarantee like Zendesk's safe update.

A procedure can start automatically when the evidence is ready.
[Automatic ticket investigations](https://bondrenai.github.io/TriageLab/guides/workspace/#automatic-ticket-investigations).

For the default review path, choose **Approve and start investigation**. The plan
binds approval to the selected evidence and run settings and expires after
15 minutes. TriageLab rereads the issue before launch; changed evidence requires
fresh review. Each tracked issue can consume one investigation attempt and
shares the workspace's single active run slot. Reports retain the GitHub issue
and comment references alongside collected process evidence.

## Share an investigation summary

Set `investigation_comments: true` to enable summaries for completed linked
investigations. On the report, choose **Review comment**. Review the linked issue,
repository audience, and exact body before choosing **Post comment**. The preview
includes the run ID, outcome, a bounded agent summary, and selected process IDs,
commands, and exit codes. Its imported text is wrapped in a literal Markdown
code block to prevent accidental mentions, links, or images. Model analysis
remains a hypothesis; the process records are the collected evidence.

The summary is an ordinary issue comment, visible to everyone with repository
access and public when the repository is public. It is not a private internal
note. A confirmed send stores its comment ID; repeating approval returns that
receipt. An uncertain delivery keeps its durable reservation and blocks another
send for the same investigation. Use the run ID to inspect the GitHub conversation.

## Limits

The connector supports GitHub.com issues, including private issue text when the
token has access. Attachments must be publicly downloadable GitHub uploads;
private attachment authentication, arbitrary linked downloads, inline code
blocks as files, and repository cloning are unsupported. Pull requests,
GitHub Enterprise hosts and inbox synchronization are also unsupported. More than 100 issue
comments, oversized responses, unsupported files, or inaccessible evidence
require manual review. GitHub API rate limits can temporarily prevent refresh.

Imported prose and files remain untrusted. Attachment validation and credential
redaction are best effort; review evidence for secrets and customer data before
sending it to a model. The existing [security model](../SECURITY.md), alpha
kagent API limitations, output bounds, watchdog latency, and cleanup reporting
also apply. Connector fixtures exercise parsing, delivery, and security
boundaries; they do not establish a live GitHub-to-kagent investigation
demonstration. No live GitHub clarification or summary sends were performed
while implementing this connector; a controlled live pilot remains to be run.

