#!/usr/bin/env bash
# Checkout session cache warm-up. Starts a local Redis with redis.conf, writes
# the session entries, and reports how many writes Redis accepted. Needs
# redis-server and redis-cli; uses loopback only and stops Redis on exit.
set -euo pipefail
cd "$(dirname "$0")"

usage() {
  echo "usage: bash warmup.sh [--policy MAXMEMORY_POLICY] [--ttl SECONDS] [--keys COUNT]" >&2
  exit 64
}

policy="" ttl="" keys=5000
while [[ $# -gt 0 ]]; do
  [[ $# -ge 2 ]] || usage
  case "$1" in
    --policy) policy="$2" ;;
    --ttl) ttl="$2" ;;
    --keys) keys="$2" ;;
    *) usage ;;
  esac
  shift 2
done
[[ "$keys" =~ ^[1-9][0-9]*$ ]] || usage
[[ -z "$ttl" || "$ttl" =~ ^[1-9][0-9]*$ ]] || usage

for tool in redis-server redis-cli; do
  command -v "$tool" >/dev/null || { echo "warm-up needs $tool on PATH" >&2; exit 127; }
done

cli=(redis-cli -h 127.0.0.1 -p 6379)
server_args=(redis.conf)
[[ -n "$policy" ]] && server_args+=(--maxmemory-policy "$policy")

rejections="$(mktemp)"
stop() {
  "${cli[@]}" shutdown nosave >/dev/null 2>&1 || true
  rm -f "$rejections"
}
"${cli[@]}" shutdown nosave >/dev/null 2>&1 || true
rm -f redis.log
trap stop EXIT
redis-server "${server_args[@]}" >/dev/null

ready=""
for _ in $(seq 50); do
  if [[ "$("${cli[@]}" ping 2>/dev/null)" == PONG ]]; then
    ready=1
    break
  fi
  sleep 0.1
done
if [[ -z "$ready" ]]; then
  echo "redis-server did not start; last log lines:" >&2
  tail -n 20 redis.log >&2 2>/dev/null || true
  exit 2
fi

info() {
  "${cli[@]}" info "$1" | tr -d '\r' | awk -F: -v field="$2" '$1 == field { print $2 }'
}

payload="$(printf 'x%.0s' $(seq 512))"
echo "redis: maxmemory=$(info memory maxmemory_human) policy=$("${cli[@]}" config get maxmemory-policy | tail -n 1)"
echo "warm-up: writing $keys session entries of ${#payload} bytes${ttl:+ with a ${ttl}s expiry}"

# One pipelined connection; redis-cli prints each rejected write on stderr.
summary="$(
  for ((i = 1; i <= keys; i++)); do
    key="session:$i"
    if [[ -n "$ttl" ]]; then
      printf '*5\r\n$3\r\nSET\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n$2\r\nEX\r\n$%d\r\n%s\r\n' \
        "${#key}" "$key" "${#payload}" "$payload" "${#ttl}" "$ttl"
    else
      printf '*3\r\n$3\r\nSET\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n' \
        "${#key}" "$key" "${#payload}" "$payload"
    fi
  done | "${cli[@]}" --pipe 2>"$rejections" || true
)"
rejected="$(sed -n 's/^errors: \([0-9]*\), replies: [0-9]*$/\1/p' <<<"$summary")"
replies="$(sed -n 's/^errors: [0-9]*, replies: \([0-9]*\)$/\1/p' <<<"$summary")"
if [[ -z "$rejected" || "$replies" != "$keys" ]]; then
  echo "warm-up could not finish: unexpected redis-cli output" >&2
  printf '%s\n' "$summary" >&2
  exit 2
fi

echo "redis: used_memory=$(info memory used_memory_human) keys=$("${cli[@]}" dbsize) evicted_keys=$(info stats evicted_keys)"
if ((rejected > 0)); then
  echo "warm-up FAILED: Redis rejected $rejected of $keys writes" >&2
  echo "first error: $(head -n 1 "$rejections")" >&2
  exit 1
fi
echo "warm-up complete: all $keys session entries written"
