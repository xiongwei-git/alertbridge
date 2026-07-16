#!/bin/sh
set -eu

project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
compose_file="$project_dir/compose.e2e.yaml"
export COMPOSE_PROJECT_NAME=${COMPOSE_PROJECT_NAME:-alertbridge-e2e-test}
tmp_dir=${E2E_TMP_DIR:-"$project_dir/test/e2e/tmp-run"}
port=${E2E_PORT:-18082}
mock_port=${E2E_MOCK_PORT:-19091}
admin_password=e2e-admin-password-strong
password_file="$tmp_dir/admin-password"

compose() {
  ALERTBRIDGE_PORT="$port" E2E_MOCK_PORT="$mock_port" ALERTBRIDGE_ADMIN_PASSWORD_FILE="$password_file" \
    docker compose -f "$project_dir/compose.yaml" -f "$compose_file" "$@"
}

cleanup() {
  compose down -v --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM
cleanup
rm -rf "$tmp_dir"
mkdir -p "$tmp_dir"
chmod 700 "$tmp_dir"
tmp_dir=$(CDPATH= cd -- "$tmp_dir" && pwd -P)
password_file="$tmp_dir/admin-password"
printf '%s\n' "$admin_password" > "$password_file"
# Linux Compose bind-mounts file-backed secrets without changing ownership.
# The private parent directory protects the host copy; read access on both
# group and other keeps the mount portable when the host file group varies.
chmod 644 "$password_file"

compose up -d --build

attempt=0
until curl -fsS "http://127.0.0.1:$port/readyz" >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 60 ]; then
    compose logs
    exit 1
  fi
  sleep 0.5
done

container_id=$(compose ps -q alertbridge)
[ "$(docker inspect "$container_id" --format '{{.Config.User}}')" = "10001:0" ] || {
  printf 'alertbridge container must run as UID 10001 with group 0\n' >&2
  exit 1
}
[ "$(docker inspect "$container_id" --format '{{.HostConfig.ReadonlyRootfs}}')" = "true" ] || {
  printf 'alertbridge container root filesystem must remain read-only\n' >&2
  exit 1
}
secret_mount_rw=$(docker inspect "$container_id" --format '{{range .Mounts}}{{if eq .Destination "/run/secrets/admin_password"}}{{.RW}}{{end}}{{end}}')
[ "$secret_mount_rw" = "false" ] || {
  printf 'bootstrap password Secret must be mounted read-only\n' >&2
  exit 1
}

login_status=$(curl -sS -o "$tmp_dir/login.html" -D "$tmp_dir/login.headers" -c "$tmp_dir/cookies.txt" -w '%{http_code}' \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode 'username=admin' \
  --data-urlencode "password=$admin_password" \
  "http://127.0.0.1:$port/admin/login")
[ "$login_status" = 303 ] || { cat "$tmp_dir/login.html"; exit 1; }

curl -fsS -b "$tmp_dir/cookies.txt" "http://127.0.0.1:$port/admin/" > "$tmp_dir/dashboard.html"
grep -q '运行概览' "$tmp_dir/dashboard.html"
csrf=$(grep -o 'name="csrf" value="[^"]*"' "$tmp_dir/dashboard.html" | head -1 | cut -d '"' -f 4)
[ -n "$csrf" ] || exit 1

curl -fsS -b "$tmp_dir/cookies.txt" "http://127.0.0.1:$port/admin/channels" > "$tmp_dir/channels-empty.html"
grep -q '还没有通知渠道' "$tmp_dir/channels-empty.html"
curl -fsS -b "$tmp_dir/cookies.txt" "http://127.0.0.1:$port/admin/routes" > "$tmp_dir/routes-empty.html"
grep -q '还没有路由规则' "$tmp_dir/routes-empty.html"
curl -fsS -b "$tmp_dir/cookies.txt" "http://127.0.0.1:$port/admin/clients" > "$tmp_dir/clients-empty.html"
grep -q '还没有客户端' "$tmp_dir/clients-empty.html"

channel_status=$(curl -sS -o "$tmp_dir/channel-response.html" -w '%{http_code}' -b "$tmp_dir/cookies.txt" \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode "csrf=$csrf" \
  --data-urlencode 'id=feishu.test' \
  --data-urlencode 'type=feishu' \
  --data-urlencode 'enabled=on' \
  --data-urlencode 'endpoint=http://mockfeishu:9090/hook' \
  --data-urlencode 'secret=e2e-signing-secret' \
  --data-urlencode 'message_type=card' \
  --data-urlencode 'keyword=AlertBridge' \
  "http://127.0.0.1:$port/admin/channels/save")
[ "$channel_status" = 303 ] || { cat "$tmp_dir/channel-response.html"; exit 1; }

route_status=$(curl -sS -o "$tmp_dir/route-response.html" -w '%{http_code}' -b "$tmp_dir/cookies.txt" \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode "csrf=$csrf" \
  --data-urlencode 'routing_key=infrastructure' \
  --data-urlencode 'severity=critical' \
  --data-urlencode 'channels=feishu.test' \
  "http://127.0.0.1:$port/admin/routes/save")
[ "$route_status" = 303 ] || { cat "$tmp_dir/route-response.html"; exit 1; }

create_status=$(curl -sS -o "$tmp_dir/client-secret.html" -w '%{http_code}' -b "$tmp_dir/cookies.txt" \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode "csrf=$csrf" \
  --data-urlencode 'id=e2e-client' \
  --data-urlencode 'routes=infrastructure' \
  --data-urlencode 'rate_limit=30' \
  --data-urlencode 'enabled=on' \
  "http://127.0.0.1:$port/admin/clients/create")
[ "$create_status" = 201 ] || { cat "$tmp_dir/client-secret.html"; exit 1; }
client_secret=$(sed -n 's/.*<pre class="secret-value" tabindex="0">\([^<]*\)<\/pre>.*/\1/p' "$tmp_dir/client-secret.html")
[ "${#client_secret}" -eq 64 ] || { printf 'client secret was not rendered once\n' >&2; exit 1; }

curl -fsS -b "$tmp_dir/cookies.txt" "http://127.0.0.1:$port/admin/clients" > "$tmp_dir/clients.html"
grep -q 'type="checkbox" name="routes" value="infrastructure" checked' "$tmp_dir/clients.html"
grep -q 'class="term-help"' "$tmp_dir/clients.html"
! grep -q 'name="routes" placeholder=' "$tmp_dir/clients.html"
curl -fsS -b "$tmp_dir/cookies.txt" "http://127.0.0.1:$port/admin/channels" > "$tmp_dir/channels.html"
grep -q '安全关键词.*已配置' "$tmp_dir/channels.html"
curl -fsS -b "$tmp_dir/cookies.txt" "http://127.0.0.1:$port/admin/routes" > "$tmp_dir/routes.html"
grep -q 'type="checkbox" name="channels" value="feishu.test"' "$tmp_dir/routes.html"
curl -fsS -b "$tmp_dir/cookies.txt" "http://127.0.0.1:$port/admin/guide?client=e2e-client" > "$tmp_dir/guide.html"
grep -q '外部服务如何调用' "$tmp_dir/guide.html"
grep -q 'X-Notify-Signature' "$tmp_dir/guide.html"
grep -q "CLIENT_ID='e2e-client'" "$tmp_dir/guide.html"
grep -Fq "BASE_URL='http://127.0.0.1:$port'" "$tmp_dir/guide.html"
grep -Fq "POST http://127.0.0.1:$port/api/v1/events" "$tmp_dir/guide.html"
! grep -q "$client_secret" "$tmp_dir/guide.html"
! grep -q '/hooks/' "$tmp_dir/guide.html"
! grep -Eq 'Gatus|Alertmanager|Grafana|Uptime Kuma' "$tmp_dir/guide.html"

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
  -H 'Content-Type: application/json' --data-binary '{}' \
  "http://127.0.0.1:$port/hooks/legacy/e2e-client")
[ "$hook_status" = 410 ] || { cat "$tmp_dir/hook-response.json"; exit 1; }
grep -q '"code":"endpoint_removed"' "$tmp_dir/hook-response.json"

attempt=0
until curl -fsS "http://127.0.0.1:$mock_port/count" | grep -q '"count":1'; do
  attempt=$((attempt + 1))
  [ "$attempt" -lt 30 ] || exit 1
  sleep 0.2
done
curl -fsS "http://127.0.0.1:$mock_port/last" > "$tmp_dir/last-notification.json"
grep -Eq '时间[^+]*\+08:00' "$tmp_dir/last-notification.json"

container_id=$(compose ps -q alertbridge)
if docker inspect "$container_id" --format '{{range .Config.Env}}{{println .}}{{end}}' | grep -Fq "$admin_password"; then
  printf 'bootstrap password leaked into container environment\n' >&2
  exit 1
fi
docker cp "$container_id:/var/lib/alertbridge-secrets/master.key" "$tmp_dir/master-key-before" >/dev/null
printf '%s\n' 'different-bootstrap-password' > "$password_file"
compose restart alertbridge >/dev/null
attempt=0
until curl -fsS "http://127.0.0.1:$port/readyz" >/dev/null 2>&1; do
  attempt=$((attempt + 1)); [ "$attempt" -lt 40 ] || exit 1; sleep 0.2
done
container_id=$(compose ps -q alertbridge)
docker cp "$container_id:/var/lib/alertbridge-secrets/master.key" "$tmp_dir/master-key-after" >/dev/null
cmp "$tmp_dir/master-key-before" "$tmp_dir/master-key-after"

original_login=$(curl -sS -o /dev/null -w '%{http_code}' -H 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode 'username=admin' --data-urlencode "password=$admin_password" "http://127.0.0.1:$port/admin/login")
[ "$original_login" = 303 ] || exit 1
changed_login=$(curl -sS -o /dev/null -w '%{http_code}' -H 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode 'username=admin' --data-urlencode 'password=different-bootstrap-password' "http://127.0.0.1:$port/admin/login")
[ "$changed_login" = 401 ] || exit 1

status=$(send_event nonce-0003)
[ "$status" = 202 ] || { cat "$tmp_dir/response.json"; exit 1; }
grep -q '"outcome":"duplicate"' "$tmp_dir/response.json"
curl -fsS "http://127.0.0.1:$mock_port/count" | grep -q '"count":1'

image_id=$(compose images -q alertbridge)
image_bytes=$(docker image inspect "$image_id" --format '{{.Size}}')
memory_usage=$(docker stats --no-stream --format '{{.MemUsage}}' "$container_id")

printf 'Docker E2E passed: empty first boot, Argon2id admin bootstrap, UI-only dynamic setup, canonical signed API, replay rejection, delivery, and key/config persistence.\n'
printf 'Docker footprint: image_bytes=%s idle_memory=%s\n' "$image_bytes" "$memory_usage"
