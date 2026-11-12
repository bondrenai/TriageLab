#!/usr/bin/env python3
"""rowferry 2.8.1, support build.

rowferry copies a table to a CSV sink one page at a time. Each page is read in
cursor order and resumes after the cursor values of the last row it wrote.

The support build keeps the release's paging and retry code and replaces the
network source drivers with replay mode. `--replay EXPORT.csv` loads a table
export into a scratch SQLite database built from the configured schema, runs
the normal sync against it, then compares the sink with the export. It needs
no network access and leaves the export untouched.
"""

from __future__ import annotations

import argparse
import csv
import json
import re
import sqlite3
import sys
import time
from pathlib import Path

VERSION = "2.8.1"
IDENTIFIER = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*$")
LIST_SETTINGS = {"cursor_columns", "sum_columns"}
INTEGER_SETTINGS = {"page_size", "lock_timeout_ms", "max_retries"}


class ConfigError(Exception):
    pass


def load_config(path: str, overrides: list[str]) -> dict:
    try:
        config = json.loads(Path(path).read_text())
    except (OSError, ValueError) as error:
        raise ConfigError(f"cannot read {path}: {error}") from error
    for item in overrides:
        key, separator, value = item.partition("=")
        if not separator or key not in config:
            raise ConfigError(f"--set needs KEY=VALUE for a key in {path}, got {item!r}")
        if key in LIST_SETTINGS:
            config[key] = [part.strip() for part in value.split(",") if part.strip()]
        elif key in INTEGER_SETTINGS:
            try:
                config[key] = int(value)
            except ValueError as error:
                raise ConfigError(f"{key} must be an integer, got {value!r}") from error
        else:
            config[key] = value
    try:
        names = [config["table"], config["primary_key"], *config["cursor_columns"], *config["sum_columns"]]
        if not config["cursor_columns"]:
            raise ConfigError("cursor_columns needs at least one column")
        if config["page_size"] < 1 or config["max_retries"] < 0:
            raise ConfigError("page_size must be positive and max_retries must not be negative")
        for required in ("schema", "sink", "lock_timeout_ms"):
            config[required]
    except KeyError as error:
        raise ConfigError(f"{path} is missing {error}") from error
    for name in names:
        if not IDENTIFIER.match(name):
            raise ConfigError(f"not a plain column or table name: {name!r}")
    return config


def load_replay(config: dict, export: str) -> sqlite3.Connection:
    """Build the scratch source database from the schema and the table export."""
    connection = sqlite3.connect(":memory:", timeout=config["lock_timeout_ms"] / 1000)
    connection.row_factory = sqlite3.Row
    connection.executescript(Path(config["schema"]).read_text())
    with open(export, newline="") as handle:
        reader = csv.reader(handle)
        header = next(reader)
        for name in header:
            if not IDENTIFIER.match(name):
                raise ConfigError(f"not a plain column name in {export}: {name!r}")
        marks = ", ".join("?" for _ in header)
        connection.executemany(
            f"INSERT INTO {config['table']} ({', '.join(header)}) VALUES ({marks})", reader
        )
    connection.commit()
    return connection


def read_page(connection: sqlite3.Connection, config: dict, after: tuple | None, stats: dict) -> list:
    """Read the next page after the given cursor values, retrying lock waits."""
    columns = ", ".join(config["cursor_columns"])
    where, parameters = "", []
    if after is not None:
        marks = ", ".join("?" for _ in after)
        where, parameters = f"WHERE ({columns}) > ({marks})", list(after)
    query = f"SELECT * FROM {config['table']} {where} ORDER BY {columns} LIMIT ?"
    attempt = 0
    while True:
        try:
            return connection.execute(query, [*parameters, config["page_size"]]).fetchall()
        except sqlite3.OperationalError as error:
            attempt += 1
            if "locked" not in str(error) or attempt > config["max_retries"]:
                raise
            stats["lock_retries"] += 1
            print(
                f"page {stats['pages'] + 1}: lock wait timeout after {config['lock_timeout_ms']} ms "
                f"on {config['table']} (attempt {attempt}/{config['max_retries']}); retrying page",
                file=sys.stderr,
            )
            time.sleep(min(0.25 * attempt, 2.0))


def sync(connection: sqlite3.Connection, config: dict) -> dict:
    stats = {"rows": 0, "pages": 0, "lock_retries": 0}
    sink = Path(config["sink"])
    sink.parent.mkdir(parents=True, exist_ok=True)
    after = None
    with sink.open("w", newline="") as handle:
        writer = csv.writer(handle)
        while True:
            rows = read_page(connection, config, after, stats)
            if not rows:
                break
            if stats["pages"] == 0:
                writer.writerow(rows[0].keys())
            writer.writerows(tuple(row) for row in rows)
            after = tuple(rows[-1][column] for column in config["cursor_columns"])
            stats["pages"] += 1
            stats["rows"] += len(rows)
            print(f"page {stats['pages']}: {len(rows)} rows, cursor -> {', '.join(str(value) for value in after)}")
    return stats


def read_rows(path: str) -> list[dict]:
    with open(path, newline="") as handle:
        return list(csv.DictReader(handle))


def plural(count: int) -> str:
    return f"{count} row" if count == 1 else f"{count} rows"


def replay_check(config: dict, export: str, list_missing: bool) -> bool:
    """Compare the sink with the export by primary key and by column sums."""
    key = config["primary_key"]
    source, written = read_rows(export), read_rows(config["sink"])
    written_keys = {row[key] for row in written}
    missing = [row for row in source if row[key] not in written_keys]
    repeated = len(written) - len(written_keys)
    print(f"replay check: export {len(source)} rows, sink {len(written)} rows")
    shortfalls = []
    for column in config["sum_columns"]:
        expected = sum(int(row[column]) for row in source)
        actual = sum(int(row[column]) for row in written)
        print(f"replay check: sum({column}) export {expected}, sink {actual}")
        if expected != actual:
            shortfalls.append(f"{column} off by {expected - actual}")
    if not missing and not repeated and not shortfalls:
        print("replay check passed: every exported row reached the sink exactly once")
        return True
    problems = []
    if missing:
        problems.append(f"{plural(len(missing))} in the export never reached the sink")
    if repeated:
        problems.append(f"{plural(repeated)} written more than once")
    detail = f" ({'; '.join(shortfalls)})" if shortfalls else ""
    print(f"replay check FAILED: {'; '.join(problems) or 'column sums differ'}{detail}", file=sys.stderr)
    if list_missing and missing:
        columns = list(source[0].keys())
        order = [*config["cursor_columns"], key]
        print("missing rows:")
        print(",".join(columns))
        for row in sorted(missing, key=lambda row: [row[column] for column in order]):
            print(",".join(row[column] for column in columns))
    return False


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="rowferry", description="Replay a table export through the sync.")
    parser.add_argument("--config", default="sync.json", help="sync configuration (default: sync.json)")
    parser.add_argument("--replay", required=True, metavar="EXPORT.csv", help="table export to sync from")
    parser.add_argument(
        "--set", action="append", default=[], metavar="KEY=VALUE", dest="overrides",
        help="override one configuration value for this run; lists are comma separated",
    )
    parser.add_argument("--list-missing", action="store_true", help="print export rows that never reached the sink")
    arguments = parser.parse_args(argv)
    try:
        config = load_config(arguments.config, arguments.overrides)
        connection = load_replay(config, arguments.replay)
    except (ConfigError, OSError, sqlite3.Error, StopIteration) as error:
        print(f"rowferry: {error}", file=sys.stderr)
        return 2
    print(
        f"rowferry {VERSION} replay: {arguments.replay} -> scratch sqlite "
        f"(table={config['table']} cursor_columns={','.join(config['cursor_columns'])} "
        f"page_size={config['page_size']})"
    )
    try:
        stats = sync(connection, config)
    except sqlite3.Error as error:
        print(f"rowferry: sync stopped: {error}", file=sys.stderr)
        return 2
    print(
        f"sync complete: {stats['rows']} rows in {stats['pages']} pages -> {config['sink']} "
        f"({stats['lock_retries']} lock retries)"
    )
    return 0 if replay_check(config, arguments.replay, arguments.list_missing) else 1


if __name__ == "__main__":
    raise SystemExit(main())
