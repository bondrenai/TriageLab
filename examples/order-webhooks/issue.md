# Webhooks not retried after partner 503s

During the partner outage on 2026-03-10 we lost about 40 order events. The
partner returned 503 for a few minutes and then recovered, but none of our
deliveries were retried. Every failure in the log says it failed after the
first attempt:

```
delivery failed after 1 attempt(s): HTTP 503 is not retryable
```

503 is definitely in our retry list and max_attempts is 5. Retries used to work.
Last sprint we moved the retry settings out of code into config.json; that's
the only change in this area I know of.

Attached: deliver.py (our offline delivery simulator, same retry loop as
prod), the config.json we deploy, and responses.json which replays what the
partner sent us (503, 503, then 200). With working retries that should deliver
on the 3rd attempt. README has the details on the config. Can you find out why
the retries stopped?
