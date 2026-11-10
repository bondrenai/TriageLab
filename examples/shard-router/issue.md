# order lookups randomly fail with "no route for customer"

For about two weeks we've been seeing `internal error: no route for customer`
on maybe 5-10% of order lookups. It looks random: the same dashboard page loads
fine for most customers and then fails for someone else. We retried a few of
the failed requests from the log and some seemed to work the second time (not
100% sure, the retry may have hit a different customer because the log
pipeline masks IDs).

Log excerpt, IDs masked by the pipeline:

```
2026-03-09T10:14:02Z router WARN internal error: no route for customer C-10***
2026-03-09T10:14:09Z router WARN internal error: no route for customer C-10***
2026-03-09T11:02:44Z router WARN internal error: no route for customer C-11***
```

We can't reproduce it locally. Running all our test customers through the
router works every single time:

    python3 route.py $(cat test-customers.txt)

Attached are route.py, the routing.json from prod (we tidied that file up
around when this started, but the tidy-up was cosmetic) and our test customer
list. Ops says all 16 shard hosts are healthy. Is this a race in the router or
a flaky DB host?
