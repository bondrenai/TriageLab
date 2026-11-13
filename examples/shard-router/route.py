"""Resolve the database shard host for each customer. Prints routes; never connects."""

import argparse
import json
import sys
import zlib
from pathlib import Path


def shard_for(customer_id: str, shard_count: int) -> int:
    # crc32 is stable across processes and hosts, unlike Python's salted hash().
    return zlib.crc32(customer_id.strip().upper().encode("utf-8")) % shard_count


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("customer_ids", nargs="+", metavar="CUSTOMER_ID")
    parser.add_argument("--config", type=Path, default=Path("routing.json"))
    args = parser.parse_args()
    config = json.loads(args.config.read_text(encoding="utf-8"))
    shard_count = int(config["shard_count"])
    failures = 0
    for customer_id in args.customer_ids:
        host = config["shards"].get(str(shard_for(customer_id, shard_count)))
        if host is None:
            print(f"internal error: no route for customer {customer_id}", file=sys.stderr)
            failures += 1
            continue
        print(f"{customer_id} -> {host}")
    return 1 if failures else 0


if __name__ == "__main__":
    raise SystemExit(main())
