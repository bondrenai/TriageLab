# Repro workspace: Larkspur missing orders

The rowferry 2.8.1 support build with the customer's files.

| File | Source |
| --- | --- |
| `rowferry.py` | 2.8.1 support build: the release's paging and retry code, with replay mode in place of the network source drivers |
| `sync.json` | The customer's sync configuration. Connection settings are replaced by the replay schema and a local sink. |
| `schema.sql` | The customer's `orders` columns and index, as SQLite DDL |
| `orders_export.csv` | The customer's export of the 625 orders that the 11 Nov 00:30 UTC run should have copied |
| `sync.log` | The customer's log of that run |

Replay the export through the sync as the customer configured it:

```sh
python3 rowferry.py --replay orders_export.csv
```

Exit status 0 means every exported row reached the sink exactly once. Status 1
means the replay check found a difference. `--list-missing` prints the rows
that never arrived. `--set KEY=VALUE` overrides one value from `sync.json` for
a single run, for example `--set page_size=500`.

The replay runs against SQLite, and the customer's source is a different
database. A replay shows what the paging code does with this data. It cannot
show lock contention, because nothing else is writing to the table.
