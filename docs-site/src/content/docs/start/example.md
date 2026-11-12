---
title: The example investigation
description: What the bundled missing-orders case demonstrates, what a recorded run did, and how to read its report.
---

The repository ships a support ticket whose own diagnosis is wrong. A
wholesale customer's nightly sync reports success, yet 11 orders worth
$24,935.20 never reach their warehouse. They explain, in detail, that lock
retries are to blame, and every fact they report is true. The conclusion is
not.

The case is fiction: the company, the people, the `rowferry` sync tool, the
orders, and the log. The defect is not simulated. The sync tool is about 200
lines of Python and SQLite, and it loses rows the way a real sync tool would.
It runs on the default sandbox with no network and no extra packages.

The ticket and the six files are in
[`examples/missing-orders`](https://github.com/bondrenai/TriageLab/tree/main/examples/missing-orders).
Read the ticket before the rest of this page, and decide what you would reply.

## What a useful investigation does

1. **Reproduces the loss** by replaying the customer's export through the sync
   as they configured it.
2. **Tests the customer's theory instead of adopting it.** If the same orders
   go missing with no lock retry, a retry cannot be what loses them.
3. **Finds what distinguishes the real cause**, and says which observation
   would tell the explanations apart.
4. **Verifies a correction** against the customer's own data, not a vague
   "increase the timeout".
5. **States its limits.** A replay is not the customer's database.

## A recorded run

The browser workspace bundles a real run of this case from 12 November 2026.
It used the default configuration and the `gpt-6.1-sol` investigator, and it
took 94 seconds from creating the sandbox to deleting it. The investigator read
five of the six files and ran four processes. TriageLab collected each one's
exit status and output from the sandbox itself:

| | What the investigator ran | Exit | What TriageLab collected |
| --- | --- | --- | --- |
| 1 | The replay as the customer configured it | 1 | 614 of 625 rows, `0 lock retries`, the shortfall to the cent, and the 11 order numbers from the ticket |
| 2 | The replay with `created_at,id` as the cursor | 0 | 625 of 625 rows |
| 3 | The replay with `page_size` 500, which the customer had tried | 1 | 622 of 625 rows, `0 lock retries`, three different orders missing |
| 4 | A comparison script the investigator wrote | 0 | Each page that lost orders ends inside a group of orders with the same timestamp, and the missing orders are the rest of that group |

Separately, and labelled as hypotheses, the model concluded:

- **Supported:** paging on `created_at` alone skips the rest of a timestamp
  group when a page ends inside it. The customer's EDI importer stamps a whole
  batch with one timestamp, so only EDI orders vanish, in runs of consecutive
  order numbers.
- **Rejected:** that a lock retry is needed to produce the loss, citing the
  two processes that lost orders with no retry as contradicting evidence.
- **Rejected:** that a larger page size fixes it.
- **Limits:** the replay uses SQLite, not the customer's database, and does not
  rule out a separate lock problem there.
- **A next action the ticket did not ask for:** the corrected cursor will not
  bring back orders that earlier runs skipped, so the affected interval needs a
  backfill.

Two more runs the same day reached the same result. Three runs are a small
sample; model behavior varies, so judge a run by its evidence rather than by
this transcript.

## Reading the report

After a run, open `reports/<case-id>/`:

- `report.json` is the canonical record: case inputs and their checksums, the
  sandbox and session identifiers, every experiment with its process ID and
  output, the validated agent answer, and the cleanup record.
- `report.md` is the readable rendering of the same data.

Both files are written even when a run fails partway. Failed experiments are
kept beside successful ones, because the corrected replay only means something
next to the one that lost orders. A `not_reproduced` outcome with honest
evidence is a result, not an error.

In the browser workspace, start with **Hypotheses considered**: each
hypothesis links to the processes it cites, and a rejected one links to the
processes that contradict it. Following a link opens the collected command,
exit status, and output, which is the part of the report the model did not
write.

## What is deliberately not proven

The bundled example is a saved run. Viewing it starts no model and no sandbox.
The report presents the cause as a hypothesis: a reproduction in the sandbox
does not establish that the customer's database fails the same way. Unit tests
and fixtures exercise failure paths; they do not claim an end-to-end
demonstration. What has actually been executed against real upstreams is
recorded in the repository's
[verification record](https://github.com/bondrenai/TriageLab/blob/main/docs/verification.md).

A smaller smoke case, a client timeout configured in the wrong unit, is in
[`examples/config-bug`](https://github.com/bondrenai/TriageLab/tree/main/examples/config-bug).
The integration tests use it.
