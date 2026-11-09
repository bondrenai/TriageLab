---
title: The example investigation
description: What the bundled Python timeout case demonstrates, and how to read its report.
---

The repository ships a small Python application with a timeout configuration
error: the code configures a health-check deadline in the wrong unit, so a
five-second allowance expires after five milliseconds. The application needs
no network or dependencies — it exists purely as a reproduction fixture.

A useful investigation:

1. **Reproduces the error** by running the health check as shipped.
2. **Identifies the units mismatch** from the configuration and the observed
   deadline.
3. **Verifies an explicit correction** — an exact timeout value, not a vague
   "increase the timeout" — by running the check again.

TriageLab's investigator works through the same steps in a standalone sandbox,
and the report separates what was *observed* (process runs, commands, exit
codes, captured output) from what the *model concluded* (the hypothesis linking
the units mismatch to the symptom).

## Reading the report

After the quickstart run, open `reports/<case-id>/`:

- `report.json` is the canonical record: case inputs and their bounds, the
  sandbox and session identifiers, every experiment with its process IDs and
  outputs, the validated agent answer, and the cleanup record.
- `report.md` is the readable rendering of the same data, safe to paste into a
  ticket or review.

Both files are written even when a run fails partway — a partial report is
more useful than none. Failed experiments are preserved alongside successful
ones; a "not reproduced" outcome with honest evidence is a result, not an
error.

## What is deliberately not proven

The separately labelled example bundled with the browser workspace is a
previously recorded live investigation. Viewing it starts no model and no
sandbox. Unit tests and fixtures exercise failure paths; they do not claim an
end-to-end demonstration. What has actually been executed against real
upstreams is recorded in the repository's
[verification record](https://github.com/bondrenai/TriageLab/blob/main/docs/verification.md).
