"""Simulate one webhook delivery with retries. Replays scripted HTTP statuses; no network."""

import argparse
import json
import sys
from pathlib import Path


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, default=Path("config.json"))
    parser.add_argument(
        "--responses",
        type=Path,
        default=Path("responses.json"),
        help="scripted HTTP statuses, one per attempt; the last one repeats",
    )
    args = parser.parse_args()
    retry = json.loads(args.config.read_text(encoding="utf-8"))["retry"]
    statuses = json.loads(args.responses.read_text(encoding="utf-8"))["statuses"]
    max_attempts = int(retry["max_attempts"])
    retryable = retry["retry_on_status"]
    for attempt in range(1, max_attempts + 1):
        status = statuses[min(attempt, len(statuses)) - 1]
        print(f"attempt {attempt}: HTTP {status}")
        if 200 <= status < 300:
            print(f"delivered after {attempt} attempt(s)")
            return 0
        if status not in retryable:
            print(f"delivery failed after {attempt} attempt(s): HTTP {status} is not retryable", file=sys.stderr)
            return 1
        print(f"retrying after {retry['backoff_ms'] * attempt} ms (simulated, no wait)")
    print(f"delivery failed after {max_attempts} attempt(s): retries exhausted", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
