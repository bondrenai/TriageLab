---
title: What is TriageLab
description: A free, Apache 2.0 tool for evidence-driven technical issue reproduction, with a Go CLI and a browser workspace.
---

TriageLab submits a technical issue to a real KAgent investigator, gives it a
disposable Agent Substrate sandbox, records experiments independently of the
model, and saves JSON and Markdown reports. It is an early, single-user proof
of concept — not a production support platform.

```text
Local issue + files → UI / Go CLI → KAgent MCP API
                          │          ├─ investigator Session → model provider
                          │          └─ Sandbox → Agent Substrate → gVisor
                          └─ tool events + process results → reports
```

## What it does

- **Collects a case.** A description, a small repository or fixture directory,
  log files, and attachments, with strict bounds (64 files, 1 MiB per file,
  4 MiB total). Symlinks, special files, traversal, and common credential
  files are rejected.
- **Runs one real investigator.** The CLI creates one standalone sandbox,
  uploads the bounded case files, and starts one KAgent investigator session.
  The investigator chooses and revises experiments through KAgent's native
  MCP process and file tools.
- **Records evidence independently.** TriageLab reads durable tool activity and
  fetches actual process status, stdout, and stderr itself. Model claims must
  reference those process IDs. Causal interpretation remains a hypothesis; the
  report does not certify root causes.
- **Reports and cleans up.** Every run writes private `report.json` and
  `report.md`, including partial results on failures. Ctrl-C and SIGTERM
  trigger cancellation and cleanup; a server-side sandbox TTL is the fallback
  for client crashes.

## What it is not

There is no database, operator, custom sandbox runtime, or agent framework.
TriageLab does not simulate the agent or execute agent-generated commands on
your host. Nothing about a failed run is discarded: a nonzero exit code never
means the report should be thrown away, and infrastructure failures are never
reported as "not reproduced."

## Where things live

- The **browser workspace** serves the same investigation workflow over a
  loopback-only, authenticated UI, with editors for KAgent agents, sandboxes,
  and egress policies.
- The **CLI** (`triagelab investigate`) is the primary interface and the same
  engine behind the workspace.
- **Ticket and issue sources** (Zendesk, GitHub, Linear) prepare helpdesk and
  issue-tracker cases through the same approval-before-run flow.

Continue with the [quickstart](\/TriageLab\/start/quickstart/) to run the included example,
or read the [CLI reference](\/TriageLab\/reference/cli/) for the full command surface.
