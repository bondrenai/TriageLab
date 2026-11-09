"""Small deterministic downstream-client simulator. No network or waiting needed."""

import argparse
import json
import sys
from pathlib import Path


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, default=Path("config.json"))
    parser.add_argument("--timeout-ms", type=int, help="Override request_timeout_ms")
    args = parser.parse_args()
    config = json.loads(args.config.read_text())
    timeout_ms = args.timeout_ms
    if timeout_ms is None:
        timeout_ms = int(config["request_timeout_ms"])
    latency_ms = int(config["simulated_response_ms"])
    print(f"request: endpoint={config['endpoint']} timeout_ms={timeout_ms}")
    print(f"simulation: downstream_response_ms={latency_ms}")
    if timeout_ms < latency_ms:
        print(
            f"TimeoutError: deadline {timeout_ms}ms elapsed before response at {latency_ms}ms",
            file=sys.stderr,
        )
        return 1
    print("HTTP 200: healthy")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
