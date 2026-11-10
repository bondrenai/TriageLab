"""Print each line and the total of an exported invoice CSV. Reads only the given file."""

import argparse
import csv
import sys
from decimal import Decimal, InvalidOperation
from pathlib import Path


def load_lines(path: Path) -> list[dict[str, str]]:
    # utf-8-sig accepts the byte-order mark that some spreadsheet exports add.
    # csv handles a final row with or without a trailing newline.
    with path.open(newline="", encoding="utf-8-sig") as handle:
        return list(csv.DictReader(handle))


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("invoice", type=Path, help="invoice export (CSV)")
    args = parser.parse_args()
    lines = load_lines(args.invoice)
    total = Decimal("0.00")
    for number, line in enumerate(lines, start=1):
        try:
            amount = Decimal(line["quantity"]) * Decimal(line["unit_price"])
        except (InvalidOperation, KeyError, TypeError) as error:
            print(f"error: line {number} is malformed: {error!r}", file=sys.stderr)
            return 2
        total += amount
        print(f"{number}. {line['description']}: {amount:.2f}")
    print(f"lines: {len(lines)}")
    print(f"total: {total:.2f}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
