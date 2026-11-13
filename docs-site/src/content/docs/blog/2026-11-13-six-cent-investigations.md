---
title: Six-cent triage, local models, and cheap escalations
description: An automated bug investigation is priced like a premium service, but the work underneath costs cents. We ran the missing-orders case on a local 27B model, priced the same tokens on hosted APIs, and looked at what a failed cheap run is still worth.
author: Will Krause
date: 2026-11-13
tags:
  - benchmarks
  - economics
  - local-models
# Social card. Source and render command: docs-site/social/.
head:
  - tag: meta
    attrs:
      property: og:image
      content: https://bondrenai.github.io/TriageLab/social/six-cent-investigations.png
  - tag: meta
    attrs:
      property: og:image:width
      content: "2400"
  - tag: meta
    attrs:
      property: og:image:height
      content: "1260"
  - tag: meta
    attrs:
      property: og:image:alt
      content: "TriageLab blog: Six-cent triage, local models, and cheap escalations. A recorded run of one sandboxed investigation on a 27B open-weight model costs $0.06 on a hosted API."
  - tag: meta
    attrs:
      name: twitter:image
      content: https://bondrenai.github.io/TriageLab/social/six-cent-investigations.png
---

Automated investigation of support tickets and bug reports is usually sold as a
premium service, priced per seat, per resolution, or per investigation. The
work underneath is modest: a model, a sandbox, a handful of tools, and a few
hundred thousand tokens that are mostly cache hits. The gap between that price
and that cost is markup, and you can skip it by running the workflow yourself.

To put a number on the cost, we ran one realistic investigation end to end on a
small open-weight model and counted every token. This post covers three things:

1. **The run.** A 27B model on a laptop correctly diagnosed a ticket whose own
   diagnosis was wrong.
2. **The cost.** Those tokens are free at the margin on local hardware and cost
   **$0.06 to $0.19** on hosted open-weight APIs.
3. **The failure case.** When a small model does not solve a ticket, its run
   still makes escalating to a frontier model cheap.

## The setup

TriageLab gives one investigator model a disposable sandbox per run, lets it
execute commands against the ticket's files, and writes a local report that
keeps what actually ran separate from what the model concluded.

We ran the bundled [`missing-orders`](/TriageLab/start/example/) case against
`Qwen3.8-27B-4bit`, a 4-bit quantized open-weight model served locally on Apple
Silicon. It got the same system prompt, strict output schema, and six sandbox
tools as the default `gpt-6.1-sol` investigator, with no domain hints added.

Switching models took no code changes. A
[custom agent definition](/TriageLab/guides/agents/) pointed at a loopback
OpenAI-compatible endpoint (`mlx-community/Qwen3.8-27B-4bit` with speculative
decoding at `medium` reasoning effort), and we raised the wall-clock timeout.

## A ticket whose own diagnosis is wrong

The case is harder than a stack-trace lookup. A customer's nightly sync reports
exit code `0`, yet 11 EDI orders worth `$24,935.20` are missing from their
warehouse. The ticket arrives with a detailed, internally consistent theory
blaming database lock-wait retries, plus six files: the ~200-line Python/SQLite
sync script (`rowferry.py`), configuration, schema, the production sync log,
and a 625-row CSV export.

Every observation in the ticket is true. The conclusion is wrong.
`rowferry.py` pages with a strict `WHERE (created_at) > (?)` cursor on a
non-unique timestamp column, and the EDI importer writes batches that share a
single microsecond timestamp. Whenever a 100-row page ends partway through a
batch, the next page skips the rest of that timestamp group.

## What the 27B model did

The run took 18 model turns and five sandbox processes. In that time the local
investigator:

- **Reproduced the exact loss** (`proc-bb05cfbf51467ee1d518776c`). 614 of 625
  rows delivered, `total_cents` short by `2,493,520` (`$24,935.20`), the same
  11 EDI order IDs, and all seven per-page cursors matching the customer's
  production log character for character.
- **Tested and rejected the lock-retry theory.** The identical 11 orders
  vanished with `0 lock retries` in the SQLite replay, and `read_page()`
  re-executes the identical query and cursor on retry.
- **Tested the customer's `page_size=500` experiment**
  (`proc-7cd305c047182112c820677e`). It still dropped three orders at the
  remaining page boundary, with zero retries.
- **Verified a configuration-only fix** (`proc-7af8f76f69d571f4d6600644`).
  Setting `cursor_columns=created_at,id` made the keyset order total and
  delivered all 625 rows with matching sums, while preserving time-ordered sink
  output.
- **Stated the replay's limits.** An isolated SQLite database with no
  concurrent writer cannot exercise real lock contention or rule out separate
  production retry bugs.

The frontier model was still better in three ways:

- **Speed.** `gpt-6.1-sol` finished in 94 seconds. The laptop took 23 minutes
  38 seconds.
- **Tool-call reliability.** An earlier local attempt died when the model
  emitted a malformed argument inside a multi-line `python3 -c` snippet.
- **One operational catch.** `gpt-6.1-sol` noticed that the customer's saved
  cursor (`00:14:05.902620Z`) already sits *after* the skipped batches, so
  recovering the historical orders means rewinding the checkpoint, not just
  resuming with the composite cursor.

## What those tokens cost

The local server logged exact token counts across all 18 turns:

| Metric | Tokens | Notes |
| --- | ---: | --- |
| Total input across 18 turns | `803,767` | Includes reading all 626 lines of `orders_export.csv` on turn 1 |
| Prefix-cached input | `752,512` | **93.6%** cache hit rate across the multi-turn tool loop |
| Uncached (fresh) input | `51,255` | Initial files, tool outputs, and turn framing |
| Output + `<think>` reasoning | `30,539` | Reasoning, tool calls, and the final structured JSON report |

Read naively, 803K input tokens makes an agent loop look expensive. But an
investigation is one growing prefix. Once the model has read the repository and
data on turn 1, turns 2 through 18 hit the prefix cache almost entirely, and
the bill is dominated by the 30K output and reasoning tokens.

On local hardware, the marginal token cost of that run is zero, and the
customer's logs and exports never leave your network. Where data policy allows
a hosted open-weight provider, the same tokens cost this much:

| Hosted `Qwen3.8-27B` provider | Input / Cached / Output (per 1M) | Equivalent run cost |
| --- | --- | ---: |
| OpenRouter (NEAR AI, cached) | `$0.04` / `$0.018` / `$1.35` | **`$0.06`** |
| OpenRouter (Chutes floor) | `$0.039` / `$0.039` / `$1.25` | **`$0.07`** |
| DeepInfra (promo, cached) | `$0.15` / `$0.038` / `$1.875` | **`$0.09`** |
| Alibaba DashScope Int'l (implicit cache) | `$0.50` / `$0.10` / `$3.00` | **`$0.19`** |
| DeepInfra (standard, no cache) | `$0.20` / — / `$2.50` | `$0.24` |
| Alibaba DashScope Int'l (no cache) | `$0.50` / — / `$3.00` | `$0.49` |

These figures are model tokens only. The sandbox adds a short-lived container
on a cluster you already run.

So the raw cost of testing a customer's theory against their own export,
rejecting a false lead, and verifying a workaround is between six and nineteen
cents. Whatever a vendor charges above that is paying for packaging, not for
compute. And at that cost, reproduction no longer has to be reserved for
high-severity escalations. You can run it on every qualifying ticket.

## When the cheap run fails

A 27B model will not solve every case. Harder bugs involve subtler state,
larger codebases, or multi-step setup, and a small model can run out of budget,
misread an output, or return `not_reproduced` or `inconclusive`.

With an agent that leaves only a chat transcript, that failed run is a sunk
cost. If you cannot trust the model's summary, a stronger model has to start
over and repeat the same discovery turns at frontier prices.

TriageLab's [report architecture](/TriageLab/articles/evidence-not-answers/)
changes that. Every run, including partial, timed-out, and unsuccessful ones,
writes [`report.json` and `report.md`](/TriageLab/reference/reports/) with two
strictly separated records:

- **Observed execution evidence**, fetched directly from the sandbox by
  TriageLab: the exact file hashes, every command array executed, its exit
  code, and its captured `stdout` and `stderr`.
- **Model hypotheses**, kept apart from the evidence and validated against
  real process IDs.

The evidence does not depend on how good the first model was. Even a failed
run has found the entry points, run the baseline reproduction, captured the
error messages, and recorded which commands exited `0` or `1`. A frontier model
picking up the ticket starts from that record instead of from a blank workspace
and a vague ticket. There are two ways to hand it over:

- **[Results chat](/TriageLab/guides/chat-mcp/).** Point `--chat` at a stronger
  model to review a finished or inconclusive report. It is a single, bounded,
  tool-free completion over the captured process outputs, with no second
  sandbox and no repeated exploration.
- **MCP.** An escalation workflow or coding agent connected to `/mcp` can call
  `get_investigation` and `get_process_evidence` to read the exact `stdout` and
  `stderr` from the first pass, then launch a targeted follow-up investigation
  only when a new experiment is needed.

## The takeaway

Run a local or sub-ten-cent model first on every qualifying ticket. Keep its
process record whether or not its hypothesis holds up. Spend frontier tokens
only on the tickets where the cheap pass falls short, and start those from the
evidence already collected. None of this needs a vendor in the middle: it is a
model endpoint, a sandbox, and a report format.
