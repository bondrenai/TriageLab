# CEL policy demonstration assets

Procedure set and issue material for the
[CEL policy demonstration](../../docs/cel-demo.md). One GitHub repository and
one ordered list of procedures. TriageLab evaluates CEL conditions over the
live issue to decide which procedure takes it, and with it which agent and
sandbox investigate, what evidence is required, and whether the run may
start without a person. Nothing is copied per queue.

| Procedure | CEL decision | Field | Runs on |
| --- | --- | --- | --- |
| `edge` | Takes an issue by label **or** title prefix, with no label at all | `match_condition` | `triagelab-investigator` in `triagelab-python` |
| `data-loss` | Takes issues labelled `data-loss` | `match_condition` | `data-loss-investigator` in `triagelab-python-replay` |
| `data-loss` | Requires the customer's schema and export only when `needs-replay` is present | `evidence[].required_when` | |
| `data-loss` | Starts the investigation itself unless `needs-review` holds it | `auto_run_condition` | |
| `triagelab` (default) | No conditions: the plain label check, unchanged | — | `triagelab-investigator` in `triagelab-python` |

## Files

| File | Role |
| --- | --- |
| `procedures.github.json` | The procedure set, in the exact shape the procedure API saves (`procedure` is the fallback, `additional_procedures` are checked first, in order). A unit test (`TestCELDemoProcedureSetRoutesConditionsAndAutoRun`) loads this file and runs the demonstration's decisions, so it cannot drift from the script. |
| `issue-edge.md` | The issue text for the title-routing beat. |
| [`examples/missing-orders`](../missing-orders/) | The issue body and attachments for the data-loss beats, as in the [GitHub issue demonstration](../../docs/demo.md#from-a-github-issue). |

Everything fictional here follows the missing-orders example: the gateway,
the customer, and the labels exist only for this demonstration.

## Deploy the second investigator

The `data-loss` procedure names an agent and a sandbox template that a fresh
workspace does not have. Create both under **Agents** (the workspace needs
`--allow-resource-writes`), and wait until kagent marks them ready; until
then the case stays in review with "The selected agent or sandbox is
unavailable".

- **Agent** `data-loss-investigator`, model `triagelab-model`, with these
  investigation instructions:

  > This case reports data loss. Treat the reporter's data as read-only:
  > never modify, delete, or rewrite the provided files; work on copies. Your
  > first experiment counts records at every stage of the reported pipeline
  > (input, after each transformation, output) so the loss is located before
  > it is explained. Prefer hypotheses that name the exact stage and
  > condition where records disappear, and confirm each with a
  > before-and-after count. Report counts exactly as the process output shows
  > them.

- **Sandbox template** `triagelab-python-replay`: the same pinned Python
  image and worker pool as `triagelab-python`, with environment defaults
  `TZ=UTC` and `PYTHONHASHSEED=0` so a replay is deterministic. Like every
  template, it has no network access unless an egress policy grants it.

## Load the procedure set

**In the workspace**: connect the repository under **Ticket cases**, then
open **Edit procedures** and add two procedures ahead of the default. Copy
the field values from `procedures.github.json`; the CEL expressions are:

```cel
"edge" in ticket.tags || ticket.subject.startsWith("[edge]")
```

```cel
"data-loss" in ticket.tags
```

```cel
"needs-replay" in ticket.tags
```

```cel
!("needs-review" in ticket.tags)
```

Check **Start automatically when evidence is ready** only on the `data-loss`
procedure, and enable automatic starts on the connection itself (GitHub
configuration `policy.auto_runs: true`) with a `max_auto_runs_per_day` cap.
The editor validates the expressions as you type and shows line and column
locations for syntax errors; simple ones also open in the condition builder.

**With GitOps**: wrap each procedure object from the JSON in a
`TicketProcedure` resource and reference them in evaluation order, following
[`deploy/gitops-example.yaml`](../../deploy/gitops-example.yaml) and the
[GitOps guide](https://bondrenai.github.io/TriageLab/guides/gitops/). The
`data-loss` resource looks like this:

```yaml
apiVersion: triagelab.bondren.ai/v1alpha1
kind: TicketProcedure
metadata:
  name: data-loss
  namespace: YOUR-NAMESPACE
spec:
  workspace: YOUR-WORKSPACE
  procedure:
    policy:
      tag: data-loss
      auto_questions: false
      max_questions: 0
      match_condition: '"data-loss" in ticket.tags'
      auto_runs: true
      auto_run_condition: '!("needs-review" in ticket.tags)'
    evidence:
      - kind: attachment
        match: .py
        required: true
        question: Please attach the sync tool (.py) from the affected deployment.
      - kind: attachment
        match: sync.json
        required: true
        question: Please attach the sync configuration (sync.json).
      - kind: attachment
        match: .txt
        required: true
        required_when: '"needs-replay" in ticket.tags'
        question: Please attach the schema and an export of the affected orders (.txt), with customer details removed, so we can replay the run.
    clarification: 'To replay the failing sync, please reply with the following:'
    default_agent: data-loss-investigator
    sandbox: triagelab-python-replay
    routes: []
```

Reference the procedures from the `TicketConnection` in this order, because
the first match wins: the edge resource, `data-loss`, then the default
timeout procedure. Set `auto_runs: true` on the connection's policy (and keep
a `max_auto_runs_per_day` cap) or the `data-loss` procedure cannot start
anything by itself.

## Prepare the demonstration issues

Create these in the demonstration repository. Tracking stays explicit: open
each issue number under **Ticket cases** and choose **Preview issue**.

| Issue | Title | Labels | Body and attachments |
| --- | --- | --- | --- |
| Edge | `[edge] Health checks time out at the edge gateway` | none | `issue-edge.md` |
| General | `Nightly order sync is silently dropping orders` | `triagelab` | [`missing-orders/ticket.md`](../missing-orders/ticket.md) |
| Data loss | `Nightly order sync is silently dropping orders` | `data-loss`, `needs-review` | `missing-orders/ticket.md` with only `rowferry.py` (or `rowferry.py.txt`) and `sync.json` attached; offer the schema and export "if you need to replay it" |

The two sync issues are the same report on purpose: one label sends it to
the general investigator, the other to `data-loss-investigator`. During the
demonstration the data-loss issue gains `needs-replay`, the author replies
with `schema.sql.txt` and `orders_export.csv.txt`, and the issue then loses
`needs-review`. Only repository collaborators can label issues; keep that in
mind when choosing conditions for a real repository.
