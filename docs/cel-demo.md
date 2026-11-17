# Demonstrating the CEL policies

A five-minute companion to the [missing-orders demonstration](demo.md). Same
workspace, same discipline: every run you show is a real one, so rehearse.
Where the missing-orders demo shows one investigation, this one shows the
decisions TriageLab makes *before* a run: which procedure a ticket belongs
to — and with it which agent and sandbox investigate — what evidence it must
carry, and whether it may start without a human. All three are CEL conditions evaluated over the live ticket, and the assets
live in [`examples/cel-policies`](../examples/cel-policies/).

> **Status.** Every decision in this script — routing by title, the choice
> of investigator, the conditional replay-data requirement, the held and released automatic start, and
> the recorded condition in the run's provenance — is exercised by a unit
> test against the exact procedure set an audience is shown
> (`TestCELDemoProcedureSetRoutesConditionsAndAutoRun` in
> `internal/ticketflow`). The workspace walkthrough itself has not yet been
> performed end to end against a live repository; it has the status the
> missing-orders form steps had before their recorded runs. When you run it,
> save the report under `docs/verification/` and describe it in
> [verification](verification.md).

## What the audience sees

One GitHub repository, one connection, three procedures. No queues and no
per-team copies of settings; ordered conditions decide.

| Moment | CEL field | What it proves |
| --- | --- | --- |
| An issue with **no labels** is routed by its title | `policy.match_condition` | Conditions read the ticket, not a copied configuration |
| The same report, labelled differently, gets a different agent and sandbox | `policy.match_condition` → `default_agent`, `sandbox` | Routing decides who investigates, not only whether |
| One label makes the customer's data mandatory | `evidence[].required_when` | Evidence rules adapt to the ticket without a second procedure |
| One label holds an automatic start; removing it releases the run | `policy.auto_run_condition` | Automatic work is gated, re-checked against the live issue, and recorded |

## Before you present

1. Bring up the stack with the [quickstart](../README.md#quickstart), start
   the [browser workspace](../README.md#browser-workspace), and mint a fresh
   token.
2. Deploy `data-loss-investigator` and the `triagelab-python-replay`
   sandbox template as described in
   [`examples/cel-policies`](../examples/cel-policies/README.md#deploy-the-second-investigator).
   Load the procedure set from
   [`examples/cel-policies`](../examples/cel-policies/README.md) with
   **Edit procedures**, and enable automatic starts on the connection
   (`policy.auto_runs: true`) with a `max_auto_runs_per_day` cap. While
   editing, type a syntax error into a condition field once: the editor
   reports it inline with line and column before you could save.
3. Create the three issues listed in that README. The two sync issues are
   the missing-orders report, one labelled `triagelab`, the other
   `data-loss` and `needs-review` with only the tool and its configuration
   attached.
4. Rehearse once: route, label, unlabel, and read the run's report end to
   end.

## The five minutes

**1. One list, three procedures (30 seconds).** Open **Edit procedures** and
show the ordered list: `edge`, `data-loss`, `triagelab`. Say: every issue from
this repository goes through this list in order, and the first procedure whose
condition matches supplies the evidence checklist, the clarification, the
agent, and the sandbox. The last one has no condition — an empty condition is
the plain label check, which is all the original product needed.

**2. Routing picks the investigator (90 seconds).** Preview the edge issue.
Point out it has **no labels**: the title starts with `[edge]`, and the
condition `"edge" in ticket.tags || ticket.subject.startsWith("[edge]")`
matched on `ticket.subject`. Then preview the two sync issues, the same
report with different labels. The `triagelab` one falls through to the
default: `triagelab-investigator` and a checklist written for Python
timeouts. The `data-loss` one gets its own procedure, `data-loss-investigator`
and `triagelab-python-replay`. Open **Agents** and show that agent's
instructions, then **Sandboxes** and the replay template's pinned `TZ` and
`PYTHONHASHSEED`. Say: routing does not only decide which tickets get
investigated; it decides who investigates them, and how.

**3. A requirement that appears (90 seconds).** The data-loss issue is ready
with the tool and its configuration. In GitHub, add the `needs-replay` label
and refresh: the case moves to review with one question, the schema and an
export of the affected orders. The `.txt` rule was there all along, optional;
`"needs-replay" in ticket.tags` made it required, so the customer's data is
only requested when someone decides a replay is needed. Reply as the issue
author with `schema.sql.txt` and `orders_export.csv.txt` and refresh: the
case is ready again, with four inputs tied to their sources. Nothing was
reconfigured between those previews — the ticket changed, and the condition
followed it.

**4. The brake pedal (one minute).** Look at the data-loss case again. Its
evidence is complete, and the reason line says the automatic start condition
did not match, so approval stays with a human: the `needs-review` label is
the brake, from `!("needs-review" in ticket.tags)`. Remove the label in
GitHub and refresh: TriageLab rereads the issue and starts the investigation
itself, on `data-loss-investigator`. Say the two properties that make this safe: the condition is
evaluated again against the live issue before launch — stale attributes never
authorize a run — and the condition text is recorded in the run's
provenance under `case.source.auto_run_condition`.

**5. While it runs, and close (one minute).** This is the missing-orders
investigation, run by `data-loss-investigator`. While it runs, use the progress view as in the main demo. At
the close, open the report: **Experiment register** for the collected
process output, **Hypotheses considered** for the model's analysis, and
**Resources** showing the sandbox deleted. The **Round 1 record** names the
agent and sandbox template; in the case source block, point at
`ticket_procedure_tag`, `ticket_agent`, and the recorded
`auto_run_condition`: the decisions that chose and started the run are in
the report, next to the evidence.

## If it goes differently

- **A condition cannot be evaluated** — for instance a map key that does not
  exist. Fail-closed is the design, not an accident: routing and evidence
  errors send the case to review, and an error never authorizes an automatic
  run; manual approval still works. Show the review state if you hit it, and
  say exactly that.
- **The wrong procedure matched.** Order decides; the first match wins. Fix
  the order or the condition and save; tracked cases pick up the change on
  the next refresh, and a changed condition invalidates any earlier approval
  plan.
- **The automatic start did not fire.** Check the three gates before assuming
  a bug: automatic starts enabled on the connection, the per-day cap, and the
  workspace's single run slot. Any of them holding the case is shown in the
  reason line; approve manually and move on.
- **The run itself goes differently.** The missing-orders guidance
  ([demo.md](demo.md#if-the-run-goes-differently)) applies unchanged.

## Questions you will get

**What can a condition read?** A `ticket` map from the current source read:
`source`, `id`, `requester_id`, `subject`, `status`, `priority`, `tags`,
`custom_fields`, `updated_at`. The
[workspace guide](https://bondrenai.github.io/TriageLab/guides/workspace/#conditions-based-on-ticket-properties)
has the table. Conditions are evaluated locally, with no network or
filesystem access, limited to 4 KiB of expression and 10,000 evaluation cost
units, and must return a boolean. Syntax and result-type errors are rejected
when saving; the editor shows locations as you type.

**Can a reporter steer their ticket into a procedure?** Ticket properties can
be requester-controlled. On GitHub, labels require triage permission, and a
title prefix is exactly as trustworthy as titles in your repo. Choose
conditions consistent with what your source lets people change, and remember
what a condition can never do: the hard limits are the sandbox, denied
egress, resource limits, and credentials that stay out of the sandbox.

**Why not one connection per queue?** Copies of settings drift, and nobody
reviews diffs between them. A condition is short text, validated once at save
against the real schema, re-evaluated against the live ticket at every step,
and recorded in the report when it starts a run.

**What is evaluated where?** Entirely inside the workspace process. Nothing
about the ticket is sent anywhere to make these decisions.

## What is real and what is not

| Real | Fiction |
| --- | --- |
| The routing, agent-selection, conditional-evidence, and automatic-start decisions, exercised by a unit test against this exact procedure set | The edge gateway and the `[edge]` title |
| The editor validation, fail-closed behavior, launch re-checks, and provenance recording | The `needs-replay` and `needs-review` workflow of the fictional support team |
| The missing-orders defect and its replay (the recorded runs used the default investigator; a completed run on `data-loss-investigator` has not yet been recorded) | — |

## What is left

- Perform this demonstration end to end against a live repository, and record
  it like the missing-orders runs (a recorded overview video `GwyqY59lKY8` is
  linked from the workspace docs).
- Pilot the same procedure set and `policy.auto_question_condition`
  (implemented and unit-tested fail-closed under the existing send controls) on
  more ticket types ([roadmap](../TODO.md)).
