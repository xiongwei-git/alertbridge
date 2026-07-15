#!/bin/sh
set -eu

endpoint=${ALERTBRIDGE_TEST_URL:-http://127.0.0.1:18081}
client_id=${ALERTBRIDGE_CLIENT_ID:-}
secret_file=${ALERTBRIDGE_CLIENT_SECRET_FILE:-}
routing_key=${ALERTBRIDGE_ROUTING_KEY:-}
severity=${ALERTBRIDGE_SEVERITY:-info}

if [ -z "$client_id" ] || [ -z "$secret_file" ] || [ -z "$routing_key" ]; then
  printf '%s\n' '请先在后台创建路由和客户端，然后设置：' >&2
  printf '%s\n' '  ALERTBRIDGE_CLIENT_ID、ALERTBRIDGE_CLIENT_SECRET_FILE、ALERTBRIDGE_ROUTING_KEY' >&2
  exit 1
fi
if [ ! -f "$secret_file" ]; then
  printf '客户端密钥文件不存在：%s\n' "$secret_file" >&2
  exit 1
fi

client_secret=$(tr -d '\r\n' < "$secret_file")
timestamp=$(date +%s)
occurred_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
event_id="local-test-$timestamp-$$"
nonce="nonce-$(openssl rand -hex 8)"
body=$(printf '{"event_id":"%s","source":"local-test","routing_key":"%s","status":"test","severity":"%s","title":"AlertBridge 本地测试","message":"本地 Docker 接收、持久化和异步投递正常。","occurred_at":"%s","labels":{"environment":"local"}}' "$event_id" "$routing_key" "$severity" "$occurred_at")
body_digest=$(printf '%s' "$body" | openssl dgst -sha256 -binary | xxd -p -c 256)
signature=$(printf '%s\n%s\n%s' "$timestamp" "$nonce" "$body_digest" | openssl dgst -sha256 -hmac "$client_secret" -hex | awk '{print $NF}')

curl -fsS \
  -H 'Content-Type: application/json' \
  -H "X-Notify-Client: $client_id" \
  -H "X-Notify-Timestamp: $timestamp" \
  -H "X-Notify-Nonce: $nonce" \
  -H "X-Notify-Signature: $signature" \
  --data-binary "$body" \
  "$endpoint/api/v1/events"
printf '\n'
