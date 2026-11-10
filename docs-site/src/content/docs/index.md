---
title: What is TriageLab
description: A Kubernetes-based tool that automates technical investigations for tickets and issues — routing, classification, and sandboxed investigation.
---

TriageLab is a Kubernetes-based tool that automates technical investigations
for your tickets and issues. When a ticket or issue arrives from Zendesk,
GitHub, or Linear, TriageLab routes it to a matching procedure, classifies
what evidence is still missing, and — after a human approves the exact
evidence and run settings — investigates the issue in a secure, disposable
sandbox. The outcome returns to the ticket with the full evidence trail.

It runs beside kagent and Agent Substrate on a dedicated cluster: kagent owns
the investigator agent and its model; Agent Substrate owns the isolated
sandbox (gVisor or microVM, egress denied by default). TriageLab owns
everything between the ticket and the report.

```text
Ticket or issue → route → classify → clarify → approve → investigate → report
                                                    └─ kagent investigator
                                                       └─ Substrate sandbox
```

## How a case moves

1. **Route.** Each source maps its tags or labels to procedures; the first
   match selects the checklist, clarification policy, agent, and sandbox
   template. Routing rules can steer different tags to different agents.
2. **Classify.** The procedure's checklist decides the facts and files the
   case needs. Facts are sanitized into evidence — credential-looking values
   redacted, provenance recorded — and missing items become reviewed
   clarification questions, sent only under an approved policy.
3. **Investigate.** One standalone sandbox is created per run, the bounded
   case files are copied in, and one kagent investigator proposes and revises
   experiments. TriageLab records every process independently: state, exit
   code, and output are fetched from the sandbox itself, never taken on the
   model's word.
4. **Report.** JSON and Markdown reports separate observed evidence from the
   model's hypothesis, and a reviewed summary can be posted back to the
   ticket. The helpdesk or issue tracker remains the conversation record.

The same workflow backs the Go CLI and the browser workspace: submit a
description and files directly, or let a ticket source drive it. Inputs are
bounded (64 files, 1 MiB each, 4 MiB total) and dangerous inputs are rejected
before any bytes move.

## Why the extra steps

Every stage in front of the sandbox exists to keep untrusted ticket content
away from the model until a person has seen exactly what will run: sanitized
facts, file hashes, chosen agent, run limits. Approval expires quickly and is
rechecked at launch, so a stale plan cannot start work. Inside the sandbox,
hard boundaries come from upstream isolation and egress policy — not from
prompting.

## Status and scope

TriageLab is an early, single-user pilot — not a production support platform.
Today all sources use the Python 3.12 timeout procedure; the workflow is
general, the procedure catalog is not yet. There is no database, operator,
custom sandbox runtime, or agent framework, and nothing is simulated: a
failed run keeps its partial report, a nonzero exit code never means the
report should be discarded, and infrastructure failures are never reported as
"not reproduced."

Continue with the [quickstart](/TriageLab/start/quickstart/) to run the
included example, or read the [CLI reference](/TriageLab/reference/cli/) for
the full command surface.
