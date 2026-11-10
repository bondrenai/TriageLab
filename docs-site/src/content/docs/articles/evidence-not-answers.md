---
title: Evidence, not answers
description: Why TriageLab records what happened separately from what the model concluded.
---

Every tool that puts a language model on a bug report eventually faces the
same question: when the model says "the timeout is misconfigured," how do you
know? TriageLab's answer is structural. The report does not contain an answer
with some evidence attached — it contains independently collected evidence,
and separately, clearly labeled, a hypothesis about what that evidence means.

## The separation

During a run, two recorders work side by side. The investigator proposes and
revises experiments through kagent's native process and file tools. TriageLab,
not the model, reads the durable tool activity, pairs each call with its
result, and fetches each process's state, exit code, and output directly from
the sandbox. A claim in the final answer must reference those observed
process IDs; a claim that references nothing observed cannot support a
"reproduced" outcome.

The consequence is a report you can audit at two levels. The process records
are ground truth: this command ran, it exited nonzero, it printed this. The
model's causal interpretation — this output is the health check failing
because five milliseconds was configured where seconds were intended — sits
beside them as a visible hypothesis for human review. The report never
certifies a root cause, because the relationship between an experiment and
the reported symptom is model-assessed.

## Negatives are results

The discipline extends to outcomes. A run that fails to reproduce the symptom
with honest, complete evidence is a `not_reproduced` result — not an error to
retry until it says something else. Failed experiments are preserved inside a
successful investigation, because a corrected run only means something next
to the run that failed. And infrastructure failures are never collapsed into
"not reproduced": if the evidence channel itself broke, the outcome is
inconclusive and the report says which part is missing.

This is also why a nonzero exit code never means "discard the report." The
most interesting runs are the ones where something went wrong, and those are
exactly the runs whose partial records you will want later.

## Why the boundary is upstream, not prompts

None of this relies on instructing the model to be honest. Imported content —
issue text, attachments, logs — is treated as untrusted at every boundary,
because prompt injection cannot be reliably defeated by more prompt. The hard
guarantees come from upstream isolation: a disposable sandbox per run,
default-deny egress, digest-pinned images, credentials kept outside the
sandbox, and resource limits enforced by the runtime rather than requested by
the agent. The model can propose anything; the sandbox decides what is
actually possible.

Evidence-first is a small design decision with a large effect on trust. It
costs a stricter report format and a more opinionated workflow. It buys you
the ability to check the work — which is the only reason to run the
investigation in the first place.
