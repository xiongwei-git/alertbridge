#!/bin/sh
set -eu

project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
compose_file="$project_dir/compose.e2e.yaml"
export COMPOSE_PROJECT_NAME=${COMPOSE_PROJECT_NAME:-alertbridge-e2e-test}
tmp_dir=${E2E_TMP_DIR:-"${TMPDIR:-/tmp}/alertbridge-e2e-test"}
port=${E2E_PORT:-18082}
mock_port=${E2E_MOCK_PORT:-19091}
runtime_gid=$(id -g)
client_secret=0123456789abcdef0123456789abcdef
admin_password=e2e-admin-password-strong

cleanup() {
  ALERTBRIDGE_GID="$runtime_gid" E2E_PORT="$port" E2E_MOCK_PORT="$mock_port" E2E_TMP_DIR="$tmp_dir" docker compose -f "$compose_file" down -v --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM
cleanup
rm -rf "$tmp_dir"
mkdir -p "$tmp_dir/secrets"
cp "$project_dir/test/e2e/config.json" "$tmp_dir/config.json"
printf '%s\n' "$client_secret" > "$tmp_dir/secrets/client-e2e"
printf '%s\n' 'http://mockfeishu:9090/hook' > "$tmp_dir/secrets/feishu-webhook"
printf '%s\n' 'e2e-signing-secret' > "$tmp_dir/secrets/feishu-signing"
printf '%s\n' "$admin_password" > "$tmp_dir/secrets/admin-password"
printf '%s\n' '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef' > "$tmp_dir/secrets/master-key"
chmod 750 "$tmp_dir" "$tmp_dir/secrets"
chmod 640 "$tmp_dir/config.json" "$tmp_dir/secrets/"*

ALERTBRIDGE_GID="$runtime_gid" E2E_PORT="$port" E2E_MOCK_PORT="$mock_port" E2E_TMP_DIR="$tmp_dir" docker compose -f "$compose_file" up -d --build

attempt=0
until curl -fsS "http://127.0.0.1:$port/readyz" >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 40 ]; then
    ALERTBRIDGE_GID="$runtime_gid" E2E_PORT="$port" E2E_MOCK_PORT="$mock_port" E2E_TMP_DIR="$tmp_dir" docker compose -f "$compose_file" logs
    exit 1
  fi
  sleep 0.5
done

login_status=$(curl -sS -o "$tmp_dir/login.html" -D "$tmp_dir/login.headers" -c "$tmp_dir/cookies.txt" -w '%{http_code}' \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode 'username=admin' \
  --data-urlencode "password=$admin_password" \
  "http://127.0.0.1:$port/admin/login")
[ "$login_status" = 303 ] || { cat "$tmp_dir/login.html"; exit 1; }
curl -fsS -b "$tmp_dir/cookies.txt" "http://127.0.0.1:$port/admin/" > "$tmp_dir/dashboard.html"
grep -q '运行概览' "$tmp_dir/dashboard.html"
curl -fsS -b "$tmp_dir/cookies.txt" "http://127.0.0.1:$port/admin/clients" > "$tmp_dir/clients.html"
grep -q 'type="checkbox" name="routes" value="infrastructure"' "$tmp_dir/clients.html"
grep -q 'class="term-help"' "$tmp_dir/clients.html"
! grep -q 'name="routes" placeholder=' "$tmp_dir/clients.html"
curl -fsS -b "$tmp_dir/cookies.txt" "http://127.0.0.1:$port/admin/channels" > "$tmp_dir/channels.html"
grep -q '安全关键词.*已配置' "$tmp_dir/channels.html"
curl -fsS -b "$tmp_dir/cookies.txt" "http://127.0.0.1:$port/admin/routes" > "$tmp_dir/routes.html"
grep -q 'type="checkbox" name="channels" value="feishu.test"' "$tmp_dir/routes.html"
curl -fsS -b "$tmp_dir/cookies.txt" "http://127.0.0.1:$port/admin/guide?client=e2e-client" > "$tmp_dir/guide.html"
grep -q '外部服务如何调用' "$tmp_dir/guide.html"
grep -q 'X-Notify-Signature' "$tmp_dir/guide.html"
grep -q 'CLIENT_ID='"'"'e2e-client'"'"'' "$tmp_dir/guide.html"
grep -Fq "BASE_URL='http://127.0.0.1:$port'" "$tmp_dir/guide.html"
grep -Fq "POST http://127.0.0.1:$port/api/v1/events" "$tmp_dir/guide.html"
! grep -q "$client_secret" "$tmp_dir/guide.html"
! grep -q '/hooks/' "$tmp_dir/guide.html"
! grep -Eq 'Gatus|Alertmanager|Grafana|Uptime Kuma' "$tmp_dir/guide.html"
csrf=$(grep -o 'name="csrf" value="[^"]*"' "$tmp_dir/dashboard.html" | head -1 | cut -d '"' -f 4)
[ -n "$csrf" ] || exit 1

create_status=$(curl -sS -o "$tmp_dir/client-secret.html" -w '%{http_code}' -b "$tmp_dir/cookies.txt" \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode "csrf=$csrf" \
  --data-urlencode 'id=e2e-dynamic-client' \
  --data-urlencode 'routes=infrastructure' \
  --data-urlencode 'rate_limit=30' \
  --data-urlencode 'enabled=on' \
  "http://127.0.0.1:$port/admin/clients/create")
[ "$create_status" = 201 ] || { cat "$tmp_dir/client-secret.html"; exit 1; }
grep -q 'e2e-dynamic-client 的新密钥' "$tmp_dir/client-secret.html"
grep -q '/admin/guide?client=e2e-dynamic-client' "$tmp_dir/client-secret.html"

timestamp=$(date +%s)
occurred_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
body=$(printf '{"event_id":"e2e-event-1","source":"e2e","routing_key":"infrastructure","status":"firing","severity":"critical","title":"E2E alert","message":"Docker delivery test","occurred_at":"%s","dedupe_key":"e2e-node"}' "$occurred_at")

send_event() {
  nonce=$1
  body_digest=$(printf '%s' "$body" | openssl dgst -sha256 -binary | xxd -p -c 256)
  signature=$(printf '%s\n%s\n%s' "$timestamp" "$nonce" "$body_digest" | openssl dgst -sha256 -hmac "$client_secret" -hex | awk '{print $NF}')
  curl -sS -o "$tmp_dir/response.json" -w '%{http_code}' \
    -H 'Content-Type: application/json' \
    -H 'X-Notify-Client: e2e-client' \
    -H "X-Notify-Timestamp: $timestamp" \
    -H "X-Notify-Nonce: $nonce" \
    -H "X-Notify-Signature: $signature" \
    --data-binary "$body" "http://127.0.0.1:$port/api/v1/events"
}

status=$(send_event nonce-0001)
[ "$status" = 202 ] || { cat "$tmp_dir/response.json"; exit 1; }
grep -q '"outcome":"queued"' "$tmp_dir/response.json"

status=$(send_event nonce-0001)
[ "$status" = 401 ] || { cat "$tmp_dir/response.json"; exit 1; }

status=$(send_event nonce-0002)
[ "$status" = 202 ] || { cat "$tmp_dir/response.json"; exit 1; }
grep -q '"outcome":"duplicate"' "$tmp_dir/response.json"

hook_status=$(curl -sS -o "$tmp_dir/hook-response.json" -w '%{http_code}' \
  -H 'Content-Type: application/json' \
  --data-binary '{}' "http://127.0.0.1:$port/hooks/legacy/e2e-client")
[ "$hook_status" = 410 ] || { cat "$tmp_dir/hook-response.json"; exit 1; }
grep -q '"code":"endpoint_removed"' "$tmp_dir/hook-response.json"
grep -q '/api/v1/events' "$tmp_dir/hook-response.json"

attempt=0
until curl -fsS "http://127.0.0.1:$mock_port/count" | grep -q '"count":1'; do
  attempt=$((attempt + 1))
  [ "$attempt" -lt 30 ] || exit 1
  sleep 0.2
done

ALERTBRIDGE_GID="$runtime_gid" E2E_PORT="$port" E2E_MOCK_PORT="$mock_port" E2E_TMP_DIR="$tmp_dir" docker compose -f "$compose_file" restart alertbridge >/dev/null
attempt=0
until curl -fsS "http://127.0.0.1:$port/readyz" >/dev/null 2>&1; do
  attempt=$((attempt + 1)); [ "$attempt" -lt 30 ] || exit 1; sleep 0.2
done

status=$(send_event nonce-0003)
[ "$status" = 202 ] || { cat "$tmp_dir/response.json"; exit 1; }
grep -q '"outcome":"duplicate"' "$tmp_dir/response.json"
curl -fsS "http://127.0.0.1:$mock_port/count" | grep -q '"count":1'
curl -fsS -b "$tmp_dir/cookies.txt" "http://127.0.0.1:$port/admin/" | grep -q '运行概览'

image_id=$(ALERTBRIDGE_GID="$runtime_gid" E2E_PORT="$port" E2E_MOCK_PORT="$mock_port" E2E_TMP_DIR="$tmp_dir" docker compose -f "$compose_file" images -q alertbridge)
container_id=$(ALERTBRIDGE_GID="$runtime_gid" E2E_PORT="$port" E2E_MOCK_PORT="$mock_port" E2E_TMP_DIR="$tmp_dir" docker compose -f "$compose_file" ps -q alertbridge)
image_bytes=$(docker image inspect "$image_id" --format '{{.Size}}')
memory_usage=$(docker stats --no-stream --format '{{.MemUsage}}' "$container_id")

printf 'Docker E2E passed: admin auth/dynamic config, canonical signed API, legacy hook removal, replay rejection, idempotency, delivery, and restart persistence.\n'
printf 'Docker footprint: image_bytes=%s idle_memory=%s\n' "$image_bytes" "$memory_usage"
