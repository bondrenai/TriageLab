# sales report crashes for the Acme profile

Since Monday the weekly report for Acme dies on the reporting server:

```
$ python3 build_report.py --profile acme --orders /data/exports/orders.csv
Traceback (most recent call last):
  File "/srv/reporting/build_report.py", line 48, in <module>
    raise SystemExit(main())
                     ^^^^^^
  File "/srv/reporting/build_report.py", line 34, in main
    raise ValueError(f"unknown column {column!r}")
ValueError: unknown column 'region_code'
```

The default profile still works on the same server with the same export.
Nothing changed in the script as far as I know; the attached build_report.py
is what we deploy from git.

I can't attach the acme profile, it lists Acme's internal account names. It
lives in profiles/ on the reporting server and I think someone on their side
edited it through the self-service page last week, but I'm not sure what they
changed. The real orders export is about 2 GB and has customer emails, so I
attached a small sample instead (I cut it down and dropped a few columns we
don't use).

What's wrong with the script? Acme wants their Monday report.
