#!/bin/sh
set -eu

project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
endpoint=${ALERTBRIDGE_TEST_URL:-http://127.0.0.1:18081}
secret_file="$project_dir/test/e2e/tmp/secrets/client-e2e"

if [ ! -f "$secret_file" ]; then
  echo "Local test stack is not initialized. Start compose.e2e.yaml first." >&2
  exit 1
fi

client_secret=$(tr -d '\r\n' < "$secret_file")
timestamp=$(date +%s)
occurred_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
event_id="local-test-$timestamp-$$"
nonce="nonce-$(openssl rand -hex 8)"
body=$(printf '{"event_id":"%s","source":"local-test","routing_key":"infrastructure","status":"test","severity":"info","title":"AlertBridge 本地测试","message":"本地 Docker 接收、持久化和异步投递正常。","occurred_at":"%s","labels":{"environment":"local"}}' "$event_id" "$occurred_at")
body_digest=$(printf '%s' "$body" | openssl dgst -sha256 -binary | xxd -p -c 256)
signature=$(printf '%s\n%s\n%s' "$timestamp" "$nonce" "$body_digest" | openssl dgst -sha256 -hmac "$client_secret" -hex | awk '{print $NF}')

curl -fsS \
  -H 'Content-Type: application/json' \
  -H 'X-Notify-Client: e2e-client' \
  -H "X-Notify-Timestamp: $timestamp" \
  -H "X-Notify-Nonce: $nonce" \
  -H "X-Notify-Signature: $signature" \
  --data-binary "$body" \
  "$endpoint/api/v1/events"
printf '\n'
