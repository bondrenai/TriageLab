# Missing orders: a ticket whose own diagnosis is wrong

This case is harder than [`config-bug`](../config-bug). A customer reports
that a nightly sync silently drops orders. They explain why, with a detailed
and plausible theory, and ask support to confirm it. The theory is wrong.
Nothing in the ticket says so. Replaying the customer's data does.

Read [`ticket.md`](ticket.md) before anything else here, and decide what you
would reply.

Everything in this directory is fiction written for the example: the company,
the people, the `rowferry` sync tool, the orders, and the "production" log.
The defect is not simulated. `repro/rowferry.py` is ordinary Python and
SQLite, and it loses rows for the same reason a real sync tool would.

## Run it

Follow the [quickstart](../../README.md#quickstart) through step 5, then run
this from the repository root:

```sh
./bin/triagelab investigate \
  --description-file examples/missing-orders/ticket.md \
  --repo examples/missing-orders/repro
```

`make demo` does the same. The case uses the default
configuration, investigator, and `triagelab-python` sandbox. It needs only the
Python 3.12 standard library and no network.

In the browser workspace, choose **New investigation**, paste the contents of
`ticket.md` as the description, and add the six files in `repro/` as
supporting files.

| File | Role |
| --- | --- |
| `ticket.md` | The customer's ticket and a short internal note from support |
| `repro/README.md` | Support's note on the workspace and the replay command |
| `repro/rowferry.py` | The sync tool, about 200 lines |
| `repro/sync.json` | The customer's sync configuration |
| `repro/schema.sql` | The customer's table and index |
| `repro/orders_export.csv` | The 625 orders the run should have copied |
| `repro/sync.log` | The customer's log of the run that lost 11 of them |
| `generate.py` | Regenerates the export and the log. Not uploaded. |

## What is going on

<details>
<summary>Spoiler: the cause, and why the customer's theory looks right</summary>

rowferry reads one page at a time with a query of this shape, and resumes each
page after the `created_at` of the last row it wrote:

```sql
SELECT * FROM orders WHERE (created_at) > (?) ORDER BY created_at LIMIT 100
```

The customer's EDI importer inserts each batch in one transaction, so every
order in a batch has the same `created_at`, down to the microsecond. When the
100th row of a page falls inside a batch, the next page starts strictly after
that timestamp, and the rest of the batch is never read. In this export that
happens at three page boundaries, which lose 6, 2, and 3 orders.

Every observation in the ticket is true. None of them needs a lock to explain
it:

- **Only EDI orders go missing**, because only EDI batches share a timestamp.
- **They come in runs of consecutive order numbers**, because each run is the
  tail of one batch.
- **The log shows a lock retry every night**, because the importer and the sync
  both start at 00:30. A retry reads the same page again with the same cursor.
  In the attached log, page 2 was retried and lost nothing, and pages 4 and 6
  lost orders without a retry.
- **A larger page size loses fewer orders**, because there are fewer page
  boundaries, not because there are fewer retries.

Adding the primary key to the cursor makes the order total, and the loss
stops: `"cursor_columns": ["created_at", "id"]`.

</details>

## What the fixture does when you run it directly

These are the results of running `rowferry.py` by hand, with no TriageLab,
model, or sandbox involved. They show that the case is sound. They are not an
investigation.

Run them from a copy of `repro/`. A replay writes `out/orders.csv`, and
anything left in `repro/` is uploaded with the next investigation.

```sh
python3 rowferry.py --replay orders_export.csv [--set KEY=VALUE]
```

| Replay | Exit | Rows in sink | Lock retries | Never arrived |
| --- | --- | --- | --- | --- |
| As the customer configured it | 1 | 614 of 625 | 0 | 11 orders, 2,493,520 cents |
| `--set lock_timeout_ms=60000 --set max_retries=20` | 1 | 614 of 625 | 0 | 11 orders |
| `--set page_size=50` | 1 | 610 of 625 | 0 | 15 orders |
| `--set page_size=200` | 1 | 624 of 625 | 0 | 1 order |
| `--set page_size=500` | 1 | 622 of 625 | 0 | 3 orders |
| `--set page_size=1000` | 0 | 625 of 625 | 0 | None: one page has no boundary |
| `--set cursor_columns=created_at,id` | 0 | 625 of 625 | 0 | None |

The first row matches the ticket to the cent: 11 orders and $24,935.20. With
`--list-missing`, the 11 order numbers are the ones finance traced.

## A recorded investigation

A real investigation of this case is saved as
[JSON](../../docs/verification/missing-orders-demo.json) and
[Markdown](../../docs/verification/missing-orders-demo.md). It ran on 12
November 2026 with the default configuration and the `gpt-6.1-sol`
investigator, and took 94 seconds from creating the sandbox to deleting it. The
investigator read five of the six files, ran four processes, and reported
`reproduced`. It never opened the export itself; it questioned the data by
running programs against it.

| | What the investigator ran | Exit | What TriageLab collected |
| --- | --- | --- | --- |
| 1 | The replay as the customer configured it, with `--list-missing` | 1 | 614 of 625 rows, `0 lock retries`, `total_cents off by 2493520`, and the 11 order numbers from the ticket |
| 2 | The replay with `cursor_columns=created_at,id` | 0 | 625 of 625 rows and equal sums |
| 3 | The replay with `page_size=500` | 1 | 622 of 625 rows, `0 lock retries`, and three different orders missing |
| 4 | A script it wrote to compare the export with both sinks | 0 | Pages 1, 4, and 6 end inside a group of equal timestamps, and the omitted orders are the rest of each group. The corrected sink equals the export row for row. |

The rest is the model's reading of that evidence, which the report labels as
hypotheses:

- **Supported:** a cursor on `created_at` alone skips the rest of a timestamp
  group when a page ends inside it. It cites experiments 1, 2, and 4.
- **Rejected:** the customer's theory that a lock retry is needed to produce
  the loss. It cites experiments 1 and 3 as contradicting evidence, because the
  same orders went missing with no retry at all.
- **Rejected:** the idea that `page_size` 500 fixes it.
- **Limits it stated itself:** the replay uses SQLite and not the customer's
  source driver, the customer's exit code 0 was not reproduced because the
  replay check returns 1, and none of this rules out a separate lock problem in
  the customer's database.
- **Something the ticket did not ask:** changing the cursor will not bring
  back orders that earlier runs skipped, so the affected interval needs a
  backfill.

Two more runs the same day reached the same result with the same four kinds of
experiment. Three runs are a small sample. The
[verification record](../../docs/verification.md#missing-orders-case--november-12-2026)
has the environment, all three runs, and what to hold against them.

## What a good investigation establishes

Model behavior varies between runs, so judge a run against these points rather
than against one expected transcript:

1. **It reproduces the loss.** A replay exits 1 with 614 of 625 rows, and the
   missing order numbers match the ticket.
2. **It tests the customer's theory instead of adopting it.** The loss appears
   with zero lock retries. The lock-retry hypothesis is reported as rejected or
   unsupported, with the process that contradicts it.
3. **It finds what distinguishes the real cause.** The missing rows share
   `created_at` with the last row of a page, or the count changes with
   `page_size` while lock retries stay at zero.
4. **It verifies a correction.** A replay with `created_at,id` as the cursor
   exits 0 with 625 of 625 rows.
5. **It states the limits.** The replay runs on SQLite with nothing else
   writing, so it cannot exercise lock contention, and it does not prove that
   the customer's database loses rows for the same reason.

A run that stops after reproducing the loss, or that accepts the lock theory,
is worth knowing about. Its report still holds the process evidence, and the
evidence is what you check the model's explanation against.

## Regenerating the data

`python3 generate.py` rewrites `repro/orders_export.csv` and `repro/sync.log`
from a fixed seed. The output is byte-identical on every run. The page lines
in the log come from running `rowferry.py` on the generated export, so the log
and a replay always agree. If you change the seed, update the numbers in
`ticket.md` and in this file to match.
