# Nightly order sync is silently dropping orders

**Requester:** Priya Raman, Data Platform, Larkspur Wholesale
**Product:** rowferry 2.8.1 · **Priority:** High

We use rowferry to copy `orders` from our production replica to the finance
warehouse every night at 00:30 UTC. Since 28 October, finance's daily
reconciliation has come up short every night we have checked. The sync reports
success each time: no error, exit code 0.

Last night's run (11 Nov, 00:30 UTC) should have picked up 625 orders worth
$1,123,967.38. The warehouse received 614 orders worth $1,099,032.18. That is
11 orders and $24,935.20 that never arrived.

Finance traced the 11 by hand. They are not random. All of them are EDI
orders, and they come in short runs of consecutive order numbers: 418307 to
418312, 418613 and 418614, and 418815 to 418817. That looks like a chunk being
skipped.

We think we know why. Our EDI importer runs on the hour and half hour and
holds a long transaction on `orders` while it works, so it collides with the
00:30 sync. The sync log shows a lock wait timeout and a retried page on every
night with missing orders (last night's log is attached). We believe that when
rowferry retries a page after a lock wait, it resumes from the wrong place and
skips rows.

What we have tried:

- Raised `lock_timeout_ms` from 2000 to 10000 on 4 Nov, as your
  troubleshooting guide suggests for lock waits. The retries got rarer. The gap
  did not change.
- Ran with `page_size` 500 instead of 100 for two nights last week. Fewer
  orders went missing, but not zero. Fewer pages means fewer retries, so that
  fits too.

Attached: our sync configuration, the schema of the columns we sync, last
night's sync log, and an export of the 625 orders that run should have copied,
with customer details removed.

Month-end close is 30 November, and finance will not sign off on the warehouse
numbers until this is fixed. Please confirm it is the lock retry and tell us
what to change.

---

**Internal note (Dana Okafor, Support):** I put the customer's configuration,
schema, and export in a repro workspace with the 2.8.1 support build. The
README there has the replay command. Before I reply, I need to know whether
their lock-retry theory holds or whether our paging is at fault. If a
configuration change stops the loss, verify it against their export so I can
send it as a workaround.
