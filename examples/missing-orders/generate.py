#!/usr/bin/env python3
"""Regenerate the synthetic data for the missing-orders case.

Writes repro/orders_export.csv and repro/sync.log. Both are fiction: a fixed
seed and no clock, so the output is byte-identical on every run. The page
lines in sync.log come from running repro/rowferry.py on the generated export,
so the "production" log and a replay always agree.

Run from this directory: python3 generate.py
"""

from __future__ import annotations

import csv
import math
import random
import re
import subprocess
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path

HERE = Path(__file__).resolve().parent
SEED = 1793
FIRST_ID = 418207
# The export covers what one nightly run should pick up: everything created
# between the previous run and this one.
PREVIOUS_RUN = datetime(2026, 11, 10, 0, 30, tzinfo=timezone.utc)
THIS_RUN = PREVIOUS_RUN + timedelta(days=1)
COLUMNS = ["id", "created_at", "channel", "account", "total_cents"]


def stamp(moment: datetime) -> str:
    return moment.strftime("%Y-%m-%dT%H:%M:%S.%fZ")


def cents(rng: random.Random, median: float, spread: float, low: float, high: float) -> int:
    dollars = min(max(rng.lognormvariate(math.log(median), spread), low), high)
    return int(round(dollars * 100))


def edi_batch_size(rng: random.Random, hour: int) -> int:
    # Retailers' purchasing systems send most EDI orders overnight, US time.
    if 5 <= hour <= 11:
        return rng.randint(5, 13)
    if 12 <= hour <= 22:
        return rng.randint(1, 6)
    return rng.randint(0, 3)


def build_orders(seed: int) -> list[dict]:
    rng = random.Random(seed)
    edi_accounts = [f"ACCT-{number:04d}" for number in rng.sample(range(100, 400), 16)]
    accounts = [f"ACCT-{number:04d}" for number in rng.sample(range(1000, 9000), 260)]
    events = []
    # The EDI importer runs on the hour and half hour and inserts each batch in
    # one transaction, so every order in a batch carries the same created_at.
    for slot in range(1, 48):
        moment = PREVIOUS_RUN + timedelta(minutes=30 * slot, seconds=rng.randint(2, 7), microseconds=rng.randrange(1_000_000))
        senders = rng.sample(edi_accounts, rng.randint(1, 3))
        for _ in range(edi_batch_size(rng, moment.hour)):
            events.append((moment, "edi", rng.choice(senders), cents(rng, 2400, 0.7, 350, 18000)))
    # Portal and sales-rep orders arrive one at a time, mostly in US business hours.
    for _ in range(336):
        if rng.random() < 0.85:
            offset = rng.triangular(12.5 * 3600, 23 * 3600, 16.5 * 3600)
        else:
            offset = rng.uniform(0, 24 * 3600)
        moment = PREVIOUS_RUN + timedelta(seconds=int(offset), microseconds=rng.randrange(1_000_000))
        events.append((moment, "portal", rng.choice(accounts), cents(rng, 620, 0.8, 60, 6000)))
    for _ in range(72):
        offset = rng.triangular(13.5 * 3600, 21.5 * 3600, 17 * 3600)
        moment = PREVIOUS_RUN + timedelta(seconds=int(offset), microseconds=rng.randrange(1_000_000))
        events.append((moment, "rep", rng.choice(accounts), cents(rng, 1900, 0.7, 200, 14000)))
    events = [event for event in events if PREVIOUS_RUN < event[0] < THIS_RUN]
    # Python's sort is stable, so a batch keeps consecutive ids.
    events.sort(key=lambda event: event[0])
    single = [event[0] for event in events if event[1] != "edi"]
    batches = {event[0] for event in events if event[1] == "edi"}
    assert len(single) == len(set(single)) and not batches & set(single), "unexpected timestamp collision"
    return [
        {"id": FIRST_ID + index, "created_at": stamp(moment), "channel": channel, "account": account, "total_cents": total}
        for index, (moment, channel, account, total) in enumerate(events)
    ]


def replay_pages() -> tuple[list[tuple[int, str]], int]:
    """Run the real tool on the export and return its page lines and row count."""
    result = subprocess.run(
        [sys.executable, "rowferry.py", "--replay", "orders_export.csv", "--set", "sink=out/generate-check.csv"],
        cwd=HERE / "repro", capture_output=True, text=True, check=False,
    )
    pages = [(int(rows), cursor) for rows, cursor in re.findall(r"^page \d+: (\d+) rows, cursor -> (\S+)$", result.stdout, re.M)]
    written = int(re.search(r"^sync complete: (\d+) rows", result.stdout, re.M).group(1))
    (HERE / "repro" / "out" / "generate-check.csv").unlink()
    (HERE / "repro" / "out").rmdir()
    return pages, written


def write_log(orders: list[dict], pages: list[tuple[int, str]], written: int) -> None:
    """Write the customer's log for the run: the same pages, plus two lock waits
    while the 00:30 EDI import still held its transaction open."""
    clock = THIS_RUN + timedelta(milliseconds=412)
    lines = []

    def emit(level: str, message: str, advance_ms: int) -> None:
        nonlocal clock
        clock += timedelta(milliseconds=advance_ms)
        lines.append(f"{clock.strftime('%Y-%m-%dT%H:%M:%S.%f')[:-3]}Z {level:<5} {message}")

    emit("INFO", "rowferry 2.8.1 starting: source=orders-replica table=orders cursor_columns=created_at page_size=100", 0)
    emit("INFO", f"resuming after cursor {stamp(PREVIOUS_RUN - timedelta(seconds=68, microseconds=369588))}", 43)
    for number, (rows, cursor) in enumerate(pages, start=1):
        if number in (1, 2):
            emit("WARN", f"page {number}: lock wait timeout after 10000 ms on orders (attempt 1/5); retrying page", 10004)
            emit("INFO", f"page {number}: {rows} rows, cursor -> {cursor}", 412 + 37 * number)
        else:
            emit("INFO", f"page {number}: {rows} rows, cursor -> {cursor}", 148 + 11 * number)
    emit("INFO", f"sync complete: {written} rows in {len(pages)} pages -> s3://larkspur-finance/orders/2026-11-10.csv (2 lock retries)", 96)
    emit("INFO", "saved cursor " + pages[-1][1], 12)
    (HERE / "repro" / "sync.log").write_text("\n".join(lines) + "\n")


def main() -> int:
    orders = build_orders(SEED)
    with (HERE / "repro" / "orders_export.csv").open("w", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=COLUMNS, lineterminator="\n")
        writer.writeheader()
        writer.writerows(orders)
    pages, written = replay_pages()
    write_log(orders, pages, written)
    total = sum(order["total_cents"] for order in orders)
    print(f"wrote {len(orders)} orders ({total} cents); the default replay writes {written}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
