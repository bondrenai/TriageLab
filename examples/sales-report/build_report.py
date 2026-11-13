"""Summarize an orders export per group using a named report profile from profiles/."""

import argparse
import csv
import json
import sys
from decimal import Decimal
from pathlib import Path

PROFILE_DIR = Path("profiles")


def load_profile(name: str) -> dict:
    path = PROFILE_DIR / f"{name}.json"
    if not path.is_file():
        available = ", ".join(sorted(p.stem for p in PROFILE_DIR.glob("*.json"))) or "none"
        print(f"error: profile {name!r} not found in {PROFILE_DIR}/ (available: {available})", file=sys.stderr)
        raise SystemExit(2)
    return json.loads(path.read_text(encoding="utf-8"))


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--profile", default="default", help="profile name in profiles/")
    parser.add_argument("--orders", type=Path, default=Path("orders-sample.csv"))
    args = parser.parse_args()
    profile = load_profile(args.profile)
    filters = profile.get("filters", {})
    with args.orders.open(newline="", encoding="utf-8") as handle:
        reader = csv.DictReader(handle)
        header = reader.fieldnames or []
        for column in [profile["group_by"], profile["amount"], *filters]:
            if column not in header:
                raise ValueError(f"unknown column {column!r}")
        totals: dict[str, Decimal] = {}
        for row in reader:
            if any(row[column] != value for column, value in filters.items()):
                continue
            key = row[profile["group_by"]]
            totals[key] = totals.get(key, Decimal("0")) + Decimal(row[profile["amount"]])
    print(f"profile: {args.profile}")
    for key in sorted(totals):
        print(f"{key}: {totals[key]:.2f}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
