---
title: Reports
description: JSON is canonical, Markdown is readable — and evidence is never mixed with hypotheses.
---

Every investigation writes a private report pair under `reports/<case-id>/`:
`report.json` (canonical) and `report.md` (readable rendering). Both are
written even when a run fails partway, including partial results — a nonzero
exit code never means the report should be discarded.

## What the report contains

- **Case inputs and bounds** — the description, transferred files with sizes
  and hashes, and the limits that applied, so the run is reproducible on
  paper.
- **Connection and resources** — endpoint, user, namespace, the sandbox and
  session identifiers, and the run settings actually used, including the
  selected agent and who started the run (`started_by`).
- **Ticket launch provenance** — the linked source, the bound plan
  (`case.source.approval_plan`), and `case.source.unattended: true` when the
  procedure started the run automatically.
- **Independently collected evidence** — durable tool activity paired by call
  and result IDs, and for each process the state, exit code, and paginated
  output TriageLab fetched itself from the sandbox. Failed experiments are
  preserved alongside successful ones.
- **The model's answer** — the validated structured result, clearly separated
  from process evidence. The relationship between an experiment and the
  reported symptom is model-assessed and visible for human review; the report
  does not certify root causes.
- **Cleanup record** — what was deleted, what failed, and any request IDs
  needed for manual recovery. New runs keep operational identities in a private
  `lifecycle.json`; `triagelab close --report` uses it. `triagelab cleanup
  --report` and the workspace's **Retry cleanup** consume the report's own
  cleanup record for runs without a lifecycle journal, and refuse a report
  whose `schema_version` is missing or is not the version the build writes.

## Reviewing the model's reasoning

New investigator answers include `experiment_assessments`: each entry links a
current-round `process_id` to the question tested, an expected observation, and
the model's interpretation. An empty expected observation means no prior
expectation was established. These entries stay under `analysis`; commands,
exit codes, and captured output remain under `experiments` and are collected
independently.

Each hypothesis retains `evidence_process_ids` for the evidence behind its
reported status. `contradicting_process_ids` identifies collected results that
challenge it, while `distinguishing_observation` describes what could distinguish
it from another explanation. An empty contradiction list means no contradicting
evidence was cited, not that the explanation is proved.

TriageLab checks references against complete, exited current-round processes.
Unavailable, truncated, timed-out, or explicitly incomplete evidence cannot
support a conclusion. Invalid contradiction references are removed and the
hypothesis is marked untested; unsupported or duplicate experiment assessments
are omitted with warnings. This validates reference provenance and completeness,
not the truth of the model's interpretation. Setup failure is not a refuted
hypothesis, and a sandbox reproduction does not establish the customer's root
cause. The emitted output schema requires these fields for kagent's strict
structured output. Retained investigator sessions using an earlier prompt can
still continue: TriageLab fills only absent explanatory fields with empty arrays
or text before validating the answer. Explicitly malformed values, including
`null`, remain invalid. Earlier saved reports without these reasoning fields
remain readable.

## Paused investigations and rounds

`paused` means evidence capture and durable sandbox suspension completed.
`pausing`, `resuming`, or `invoking` describe unfinished operations, not a safe
pause. An uncertain model invocation is never automatically replayed.

The top-level report describes the latest `round` and keeps the original `case`.
`continuation` records the added information for a later round; `rounds` contains
previous reports without recursive history. Immutable copies also live in
`rounds/0001/report.json` and `report.md`, and so on. Process handles are scoped
to their round because suspension resets the guest process registry.

Keep the whole private run directory to resume. Downloads contain redacted
evidence reports; they do not include the operational lifecycle record. See
[pause and resume](/TriageLab/guides/pause-resume/) for the workflow and storage
requirements.

## Provenance and redaction

Evidence carries its source references — which file came from which reply or
upload, which tool event produced which output. Ticket and issue sources
record sanitized-fact provenance: credential-looking assignments and token
shapes are redacted before evidence enters a run, and the applied redaction
classes are recorded with the case source. Redaction is best effort; review
support data before it reaches a model provider.

## Reading outcomes

The outcome field separates what happened from why:

- `reproduced` — the symptom was observed under recorded conditions.
- `not_reproduced` — an honest negative result with evidence.
- inconclusive — missing, truncated, or failed infrastructure evidence cannot
  support either claim; infrastructure failures are never reported as "not
  reproduced."

Exit codes map onto these outcomes — see the [CLI reference](/TriageLab/reference/cli/)
for the full table.
