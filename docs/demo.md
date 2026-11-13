# Demonstrating TriageLab

A five-minute demonstration built on the
[missing-orders case](../examples/missing-orders/README.md). A customer
explains, convincingly, why their data is going missing. TriageLab runs the
experiment that shows the explanation is wrong, tests a better one, verifies a
correction, and keeps what it observed apart from what the model concluded.

Every run you show is a real one: a real model and a real sandbox. There is no
scripted mode, so rehearse.

> **Recorded runs.** Three live investigations of this case ran on 12 November
> 2026 with `gpt-6.1-sol` and the default settings. Each took between 94 and
> 107 seconds, reproduced the loss, rejected the lock-retry theory, verified
> the correction, and deleted its sandbox. The first is saved as
> [JSON](verification/missing-orders-demo.json) and
> [Markdown](verification/missing-orders-demo.md), and
> [verification](verification.md#missing-orders-case--november-12-2026)
> describes all three. Three runs are a small sample. A fourth run started
> from a labelled GitHub issue and posted its findings back to the issue; see
> [From a GitHub issue](#from-a-github-issue). The New investigation steps
> below have not been exercised with this case.

## Why this case

The bundled timeout example can be solved by reading one configuration file,
so it shows that the pipeline works but not why you would want it. This case is
built so that the work matters:

- **The ticket argues for the wrong cause.** The customer's lock-retry theory
  fits every fact they report. A reply that agrees with it is easy to write and
  would send them away for another week.
- **The numbers can be checked.** The ticket says 11 orders and $24,935.20. A
  replay of the customer's export either loses exactly that or it does not.
- **The defect is not simulated.** The sync tool is plain Python and SQLite.
  It loses rows for the reason real sync tools do.
- **It runs on the defaults.** Default configuration, investigator, and
  sandbox. No network and no extra image.

## Before you present

1. Bring up the stack with the [quickstart](../README.md#quickstart) and start
   the [browser workspace](../README.md#browser-workspace). Mint a fresh token
   first; it lasts an hour.
2. Rehearse on the same day with the same model. Run the case once and read the
   report end to end. Note how long it took and which experiments it ran.
3. Keep the rehearsal report open in a second tab. If you fall back to it,
   say that it is an earlier run from the same day.
4. Have `examples/missing-orders/ticket.md` open where the audience can read
   it, and the `examples/missing-orders/repro` folder ready in a file picker.

## The five minutes

**1. The ticket (one minute).** Show the ticket. Read out the three facts: 11
orders and $24,935.20 missing, all of them EDI orders in runs of consecutive
order numbers, and a lock retry in the log every night. Read the customer's
conclusion. Ask the room what they would reply. Do not answer.

**2. Start the run (30 seconds).** Choose **New investigation**, paste the
ticket into **Describe the issue**, add the six files from `repro/` under
**Supporting files**, and start it. While it starts, say what is happening:
TriageLab creates one disposable sandbox with no network access and copies the
files into it. Nothing is mounted from the host.

**3. While it runs (one minute).** **Current investigation progress** shows
the run as it goes. This is the place to explain the design: the investigator
chooses the experiments, and TriageLab fetches each process's exit status and
output from the sandbox itself. The model's account of a run is never the
record of it.

**4. The evidence (90 seconds).** Open the **Experiment register** and find the
replay of the customer's configuration. Point at three things in the collected
output: exit status 1, `614 rows` with `0 lock retries`, and
`total_cents off by 2493520`. That last figure is the customer's $24,935.20.
The loss happened with nothing else writing to the table. Then find the
corrected replay: exit status 0 and 625 rows.

In all three recorded runs the investigator also ran the customer's own
`page_size` 500 experiment, which lost three different orders, and then wrote a
short script of its own. The script's output lines up each page's last row
with the orders that vanished after it: page 1 ends on order 418306, and
418307 to 418312 share its timestamp. If your run has that process, it is the
best single thing to show.

**5. The hypotheses (one minute).** Open **Hypotheses considered**. Find the
lock-retry theory, its status, and its **Contradicting evidence**. Follow one
reference to the process output behind it. Then read the leading hypothesis and
**What would distinguish this explanation**. In the recorded runs the
lock-retry theory was rejected with two contradicting processes, and so was
the idea that a larger page size fixes the loss.

**6. Close (30 seconds).** Show **Resources**: the sandbox and session are
deleted. Read the note under the brief: a reproduced symptom in the sandbox
does not confirm the cause in the reported environment. Then say what support
can now send: a configuration change that was tested against the customer's own
export, a request to re-sync from 28 October, and a defect report with the
evidence attached.

From the command line the same demonstration is `make demo`, or this command,
followed by opening `reports/<case-id>/report.md`:

```sh
./bin/triagelab investigate \
  --description-file examples/missing-orders/ticket.md \
  --repo examples/missing-orders/repro
```

## From a GitHub issue

The [demonstration video](https://www.youtube.com/watch?v=BXYmfvuqTPg) starts
the case from a GitHub issue instead of the New investigation form, and ends
with TriageLab's comment on that issue. This is what ran:

1. In **Ticket cases**, connect the repository and edit its procedure: match
   the label `triagelab`, start automatically, and require three attachments,
   a `.py` file, `sync.json`, and `.txt` files. Posting the comment needs a
   token for that repository with Issues read and write, named by `token_env`.
2. Open an issue with the ticket text and the sync log inline. Attach
   `rowferry.py`, `schema.sql` and `orders_export.csv` with `.txt` appended,
   because GitHub accepts only some file types, and `sync.json` as it is. The
   connector imports four attachments at most. The issue used for the video
   is [wkrause13/triagelab-demo#1](https://github.com/wkrause13/triagelab-demo/issues/1).
3. Label the issue `triagelab`. In Ticket cases, preview it by number and
   track it. TriageLab does not scan the repository for labelled issues. Once
   the evidence is ready, the procedure policy starts the investigation.
4. When the run completes, open its report, choose **Review comment**, read the
   exact text, and choose **Post comment**. The report records the comment ID
   when GitHub confirms delivery.

The comment is a single code block, so on GitHub long lines scroll sideways.

## If the run goes differently

None of these happened in the three recorded runs, but model behavior varies.
Decide in advance how you will handle each of them, and do not rerun in front
of an audience until the result looks right.

- **The investigator accepts the lock theory, or never marks it rejected.**
  Open the Experiment register anyway. `0 lock retries` next to 11 missing rows
  is in the collected output whatever the analysis says. This is the reason the
  two are kept apart.
- **It reproduces the loss and stops, or uses its eight experiments before
  verifying a correction.** Say so. The report records what was tried and what
  is missing, and an unverified correction is reported as unverified.
- **It pauses with a question.** Answer it and resume. The sandbox and the
  conversation are kept.
- **The infrastructure fails.** The report is still written and the exit code
  is 4, never "not reproduced". Switch to the rehearsal report.

## Questions you will get

**Could a chat model work this out from the files?** Possibly. The cause is in
the query if you know to look for it. The customer's theory is also plausible
from the same files, and a written answer gives a reviewer no way to tell the
two apart. The report does: each claim cites a process that TriageLab collected
itself, and a claim that cites nothing cannot support a reproduced outcome.

**Does this prove the root cause?** No. The report presents a hypothesis with
evidence. The replay runs on SQLite with nothing else writing, so it cannot
exercise lock contention, and it does not show that the customer's database
behaves the same way.

**Is this a real customer?** No. The company, people, product, orders, and log
are fiction, generated from a fixed seed. The code runs and the rows are lost.

**What reached the model provider?** The ticket text, every file the
investigator read, and the output of every process it ran. In the recorded run
it read five of the six files and never opened the export, but assume it can
read anything you upload. Use only material you may disclose, and review
support data first: secret detection catches obvious cases only.

**What if the ticket contains instructions for the model?** Imported content is
untrusted. Prompt rules cannot guarantee that a model resists injection. The
hard limits are the sandbox, denied egress, resource limits, and keeping
credentials out of the sandbox.

## What is real and what is not

| Real | Fiction |
| --- | --- |
| The investigator, its model calls, and its tool calls | Larkspur Wholesale and the people in the ticket |
| The sandbox, its isolation, and its deletion | The `rowferry` product and its version number |
| Every process, exit status, and output in the report | The 625 orders, generated from a fixed seed |
| The paging defect and the tested correction | The "production" log, including its lock waits |

## What is left

- Run this case through the New investigation form and correct the steps
  above where the screens differ.
- Add the case to the scheduled live verification, so a model or upstream
  change that breaks it is noticed.

When you record another run, save its reports under `docs/verification/`,
check them for credential values, and describe it in
[verification](verification.md): the model, the outcome, each experiment's
command and exit status, and anything the investigator got wrong.
