---
title: Browser workspace
description: Run the loopback-only workspace UI, forward it over SSH, and iterate on it.
---

The browser workspace is the same investigation workflow as the CLI behind a
small Bondren-styled UI: case inputs and recorded experiments, not dashboard
metrics. It serves history, live runs, evidence inspection, and report
downloads from the same `reports/` directory the CLI writes.

## Run it

Use Node.js 24 and Go 1.27.3 on the development VM:

```sh
make install-ui
make build-ui
make build
bin/triagelab serve --config triagelab.example.json --output reports
```

The default listener is `127.0.0.1:8080` and `--assets` defaults to `web/dist`.
The server binds only to loopback; reach it through an SSH local forward:

```sh
ssh -N -L 8080:127.0.0.1:8080 YOUR_DEV_VM
```

Open `http://127.0.0.1:8080`. Sign-in is required — see
[sign-in and roles](/TriageLab/guides/sign-in/). The UI lists existing CLI reports in the
selected output directory, including a separately labelled example bundled
from the repository's recorded verification report. Viewing history or the
example does not require a running model provider.

To investigate, configure kagent and its model credential as in the
[quickstart](/TriageLab/start/quickstart/), forward the controller port on the VM, and
set `TRIAGELAB_TOKEN` in the server's environment before starting it. Neither
that token nor the model key belongs in browser configuration. Model and cloud
usage can incur charges even though TriageLab is free.

## Automatic ticket investigations

Ticket cases default to **Approve and start investigation**. To let a matching
procedure start runs automatically, set `policy.auto_runs` to `true` in the
ticket source JSON passed to `serve --tickets` and restart the workspace. The
default procedure uses that setting when no procedure has been saved. For
saved procedures, an admin can enable **Start automatically when evidence is
ready** under **Ticket cases → Edit procedures**.

### Conditions based on ticket properties

Set the optional `policy.auto_run_condition` expression in the source JSON,
or edit **Automatic start condition** in a saved procedure.
For example, a Zendesk procedure can allow only high-priority tickets with a
specific tag.

```cel
ticket.priority == "high" && "reproduce" in ticket.tags
```

The corresponding policy fields are shown below. Keep the source connection
settings and other policy fields in your configuration.

```json
{
  "auto_runs": true,
  "auto_run_condition": "ticket.priority == \"high\" && \"reproduce\" in ticket.tags"
}
```

Use **Condition builder** to pick ticket properties and combine them with
**All conditions** or **Any condition**. Switch to **Raw CEL** to see the
generated expression or write your own. The Monaco editor highlights CEL,
completes ticket properties as you type `ticket.`, and shows property help on
hover. Press **Ctrl+Space** for suggestions. Zendesk procedures also suggest
`priority` and `custom_fields`; custom-field IDs use bracket notation, such as
`ticket.custom_fields["123"]`.

The server checks the expression as you type. Errors appear inline and in a
clickable list with line and column locations. **Tab** moves to the next control.
Preview the draft against a ticket to check how its actual attributes affect the
case. Suggestions describe the supported properties; they do not discover
custom-field IDs or replace validation against a real ticket.

CEL can also shape ticket preparation:

| Editor | JSON field | Effect |
| --- | --- | --- |
| Procedure match condition | `policy.match_condition` | The first matching procedure supplies the investigator and sandbox template, which determines the image. A CEL condition takes the place of the tag check. The tag becomes the procedure label. Status, priority, and configured custom-field filters still apply. |
| Conditional requirement | `evidence[].required_when` | A required evidence item is mandatory when its condition returns true. Otherwise the item is optional. |
| Automatic start condition | `policy.auto_run_condition` | A ready ticket starts automatically when the condition returns true and automatic starts are enabled. |

A match or evidence condition that cannot be evaluated sends the case to
review. Preview shows the selected sandbox and any missing evidence.
An empty match condition uses the tag check. An empty evidence condition
keeps the item's required or optional setting.

CEL receives a `ticket` map with these properties from the current source read.

| Property | Value |
| --- | --- |
| `source` | `zendesk`, `github`, `linear`, `bondren`, or `jira` |
| `id`, `requester_id` | Integer identifiers used by the connector |
| `subject` | Ticket title |
| `status` | Connector status, including Linear state type and Jira status-category key |
| `priority` | Zendesk priority, or an empty string for connectors that do not collect it |
| `tags` | List of tags or labels, empty when none are present |
| `custom_fields` | Map of Zendesk custom-field IDs as string keys to their JSON values, empty for other connectors |
| `updated_at` | Source update timestamp as a string |

Guard optional fields before accessing them. JSON numeric custom-field values
are doubles, and other values retain their JSON types.

```cel
"84" in ticket.custom_fields && ticket.custom_fields["84"] == "python"
```

The condition must return a boolean. Empty conditions preserve the existing
behavior of starting when the procedure's evidence requirements are met.
A false result or evaluation error leaves the ready case available for manual
approval. Preview shows the decision without launching. Tracking, refreshing,
and polling evaluate current attributes, and launch confirms source freshness.
Changes to the condition invalidate the previous plan.

Conditions are evaluated locally with CEL and have no network or filesystem
access. Expressions are limited to 4 KiB and 10,000 evaluation cost units.
Syntax and result-type errors are rejected when saving or loading a procedure.
Only admins can edit saved policies. The automatic-launch report records the
condition under `case.source.auto_run_condition`. Ticket properties can be
requester-controlled, so choose conditions consistent with your source permissions.

Preview shows the proposed action. Tracking or refreshing a case can launch
it once the required evidence is present, the investigator and sandbox are
available, and TriageLab verifies the source is current. With polling
enabled, the workspace also checks tracked cases and can start a run after a
requester reply or when the shared run slot becomes free. Each tracked case
gets one investigation attempt. Pause tracking to suspend automatic checks
and launch for that case.

Set the optional top-level `max_auto_runs_per_day` to cap automatic launches
in a rolling 24-hour window. The workspace counts launches across restarts.
When the limit is reached, a user can approve the run or wait. Reports
record policy-started runs and keep the case linked to its source. Clarification
and summary delivery use their existing review controls.

External workflows and agents can start standalone runs through the CLI or
HTTP MCP. [Start investigations from a policy workflow](/TriageLab/guides/chat-mcp/#start-investigations-from-a-policy-workflow).

## Operate it

- The server accepts **one investigation at a time**. Closing a tab leaves a
  run going; use **Cancel** to stop it.
- Server shutdown cancels active work and allows bounded cleanup. After an
  abrupt crash, inspect the report and use `triagelab cleanup` if resources
  remain.
- The workspace can also show live kagent deployment status and save kagent
  resources — see [defining agents](/TriageLab/guides/agents/).

## Iterate on the UI

Keep the Go server running and start `make dev-ui` on the VM. Vite listens on
loopback port 5173 and proxies `/api` to the Go server; forward that port too
and open `http://127.0.0.1:5173` for hot reload. Before landing a change, run
`make check-ui`, `make build-ui`, and `make check` on the VM.

The API is a local interface for a small team, not a hosted service. Report
text renders as text, never executable HTML; the Go service normalizes uploads
with the same ingestion package as the CLI and invokes the same investigator.
No database, queue service, browser model SDK, or alternate execution backend
is introduced.
