#!/usr/bin/env bash
# End-to-end OAuth API client smoke test against a served workspace.
#
# Serves the UI with an unreachable investigator endpoint (the auth surface is
# what is under test), then walks the full client path: admin creates an API
# client over HTTP, a curl client exchanges the secret at /oauth/token,
# launches a run with the Bearer token, reads and downloads the report, and
# loses access after revocation and after the client is disabled.
#
# Usage: WORKDIR=/work/TriageLab-oauth PORT=18086 scripts/oauth-smoke.sh
set -euo pipefail
WORKDIR="${WORKDIR:-$(pwd)}"
PORT="${PORT:-18086}"
cd "$WORKDIR"
export GOTOOLCHAIN="${GOTOOLCHAIN:-go1.27.3}"
make build >/dev/null
WS="$(mktemp -d /dev/shm/tl-oauth-smoke.XXXXXX)"
trap 'kill "${SERVE_PID:-0}" 2>/dev/null || true; rm -rf "$WS"' EXIT
mkdir -p "$WS/reports"
# Point the investigator at a closed port so the run fails fast without
# contacting any real cluster.
python3 - "$WS/smoke.json" << 'PY'
import json, sys
config = json.load(open("triagelab.example.json"))
config["endpoint"] = "http://127.0.0.1:9/mcp"
json.dump(config, open(sys.argv[1], "w"), indent=2)
PY
echo 'smoke-password-1' | bin/triagelab user add --username admin --role admin --password-stdin --output "$WS/reports" >/dev/null
B="http://127.0.0.1:$PORT"
TRIAGELAB_TOKEN=smoke-not-a-real-token bin/triagelab serve --config "$WS/smoke.json" \
  --output "$WS/reports" --assets web/dist --listen "127.0.0.1:$PORT" >"$WS/serve.log" 2>&1 &
SERVE_PID=$!
for _ in $(seq 1 50); do curl -sf "$B/healthz" >/dev/null && break; sleep 0.2; done

json() { python3 -c "import json,sys;print(json.load(sys.stdin)$1)"; }
MARK='X-TriageLab-Request: 1'

echo "== 1. admin signs in"
curl -sc "$WS/cookies" -H "$MARK" -d '{"username":"admin","password":"smoke-password-1"}' "$B/api/auth/login" | json '["role"]'

echo "== 2. admin creates an API client"
CREATED=$(curl -sb "$WS/cookies" -H "$MARK" \
  -d '{"id":"ci-smoke","scopes":["investigations:run","investigations:read"]}' \
  "$B/api/auth/admin/clients")
CLIENT_SECRET=$(echo "$CREATED" | json '["client_secret"]')
echo "client_id=$(echo "$CREATED" | json '["client_id"]') secret=${CLIENT_SECRET:0:8}…"

echo "== 3. client gets a token (Basic auth, no CSRF marker, no Origin)"
TOKEN=$(curl -su "ci-smoke:$CLIENT_SECRET" -d grant_type=client_credentials "$B/oauth/token" | json '["access_token"]')
echo "access_token=${TOKEN:0:10}…"

echo "== 4. a refused route"
curl -so /dev/null -w "settings via Bearer: %{http_code} (403 expected)\n" -H "Authorization: Bearer $TOKEN" "$B/api/settings"

echo "== 5. launch an investigation as the client"
RUN=$(curl -s -H "Authorization: Bearer $TOKEN" -F "description=Smoke: service logs 500s for EU cards since 14:20" "$B/api/runs")
RUN_ID=$(echo "$RUN" | json '["id"]')
echo "run=$RUN_ID"

echo "== 6. a narrowed read-only token cannot launch"
READTOK=$(curl -su "ci-smoke:$CLIENT_SECRET" -d grant_type=client_credentials \
  --data-urlencode "scope=investigations:read" "$B/oauth/token" | json '["access_token"]')
curl -so /dev/null -w "read-only launch: %{http_code} (403 expected)\n" \
  -H "Authorization: Bearer $READTOK" -F "description=x" "$B/api/runs"

echo "== 7. poll the run until it settles"
STATUS=queued
for i in $(seq 1 "${POLLS:-30}"); do
  STATUS=$(curl -s -H "Authorization: Bearer $TOKEN" "$B/api/runs/$RUN_ID" | json '["status"]')
  echo "  poll $i: $STATUS"
  case "$STATUS" in
    error|completed|interrupted|timed_out) break ;;
  esac
  sleep 1
done
echo "started_by=$(curl -s -H "Authorization: Bearer $TOKEN" "$B/api/runs/$RUN_ID" | json '["started_by"]')"

echo "== 8. list and download through the API"
curl -s -H "Authorization: Bearer $TOKEN" "$B/api/runs" | python3 -c 'import json,sys;print("  listed:", [r["id"] for r in json.load(sys.stdin)["runs"]])'
curl -so "$WS/report.json" -w "  report.json download: %{http_code}\n" -H "Authorization: Bearer $TOKEN" "$B/api/runs/$RUN_ID/report.json"

echo "== 9. revocation ends the token"
curl -su "ci-smoke:$CLIENT_SECRET" -d "token=$TOKEN" -o /dev/null -w "  revoke: %{http_code}\n" "$B/oauth/revoke"
curl -so /dev/null -w "  API call after revoke: %{http_code} (401 expected)\n" -H "Authorization: Bearer $TOKEN" "$B/api/runs"

echo "== 10. a new token dies with the disabled client"
TOKEN2=$(curl -su "ci-smoke:$CLIENT_SECRET" -d grant_type=client_credentials "$B/oauth/token" | json '["access_token"]')
curl -sb "$WS/cookies" -H "$MARK" -X PUT -d '{"disabled":true}' "$B/api/auth/admin/clients/ci-smoke" >/dev/null
curl -so /dev/null -w "  API call after disable: %{http_code} (401 expected)\n" -H "Authorization: Bearer $TOKEN2" "$B/api/runs"
curl -su "ci-smoke:$CLIENT_SECRET" -d grant_type=client_credentials -o /dev/null -w "  token for disabled client: %{http_code} (401 expected)\n" "$B/oauth/token"

echo "== 11. files on disk"
ls -l "$WS/reports/.auth/" | awk '{print "  ", $1, $NF}'

echo "== 12. serve log lines from the protocol library"
grep -E "registered route" "$WS/serve.log" | sed 's/^/  /'
echo SMOKE_OK
