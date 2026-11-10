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
- **Independently collected evidence** — durable tool activity paired by call
  and result IDs, and for each process the state, exit code, and paginated
  output TriageLab fetched itself from the sandbox. Failed experiments are
  preserved alongside successful ones.
- **The model's answer** — the validated structured result, clearly separated
  from process evidence. The relationship between an experiment and the
  reported symptom is model-assessed and visible for human review; the report
  does not certify root causes.
- **Failure reason** — when the investigator's task stops without completing,
  the reason KAgent gives is kept as `task_failure`. It is upstream text, so it
  is clipped and redacted like the rest of the report and stays out of logs.
- **Cleanup record** — what was deleted, what failed, and any request IDs
  needed for manual recovery. `triagelab cleanup --report` consumes exactly
  this record.

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

Exit codes map onto these outcomes — see the [CLI reference](\/TriageLab\/reference/cli/)
for the full table.
