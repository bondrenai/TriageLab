# retention job deleted our newest backup??

Our nightly retention job is supposed to keep the 3 most recent orders-db
backups and prune anything older. Thursday morning the newest one,
orders-db-2026-03-04.dump, was gone from the bucket. It looks like the sort is
reversed and the job prunes the newest instead of the oldest.

How it runs: cron calls `python3 prune.py --catalog catalog.json` and the
wrapper deletes every object named on a `prune:` line. Attached is prune.py and
the catalog.json snapshot from Wednesday night, taken a few minutes before the
job ran. Someone also took a manual backup on the 2nd during the migration if
that matters.

We've turned the job off until this is sorted. Can you confirm the ordering
bug and what the fix should be? Losing the newest backup is the worst possible
failure for us.
