"""Plan backup retention from a catalog snapshot. Prints `keep:` and `prune:` lines.

This planner never deletes anything. The nightly cron wrapper deletes each
object named on a `prune:` line. After planning, the catalog is verified: every
entry must carry the sha256 recorded by the backup job.
"""

import argparse
import json
import sys
from datetime import datetime
from pathlib import Path


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--catalog", type=Path, default=Path("catalog.json"))
    parser.add_argument("--keep", type=int, default=3, help="number of newest backups to keep")
    args = parser.parse_args()
    if args.keep < 1:
        parser.error("--keep must be at least 1")
    catalog = json.loads(args.catalog.read_text(encoding="utf-8"))
    backups = sorted(
        catalog["backups"],
        key=lambda backup: datetime.fromisoformat(backup["created"]),
        reverse=True,
    )
    for backup in backups[: args.keep]:
        print(f"keep: {backup['name']} (created {backup['created']})")
    for backup in backups[args.keep :]:
        print(f"prune: {backup['name']} (created {backup['created']})")
    unverified = [backup["name"] for backup in backups if not backup.get("sha256")]
    for name in unverified:
        print(f"error: {name} has no sha256 in the catalog", file=sys.stderr)
    if unverified:
        print("catalog verification failed", file=sys.stderr)
        return 2
    print("catalog verified")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
