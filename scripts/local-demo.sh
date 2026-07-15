#!/bin/sh
set -eu

project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
compose_file="$project_dir/compose.e2e.yaml"
tmp_dir="$project_dir/test/e2e/tmp"
runtime_gid=$(id -g)
action=${1:-up}

compose() {
  ALERTBRIDGE_GID="$runtime_gid" docker compose -f "$compose_file" "$@"
}

initialize() {
  umask 077
  mkdir -p "$tmp_dir/secrets"
  cp "$project_dir/test/e2e/config.json" "$tmp_dir/config.json"
  [ -s "$tmp_dir/secrets/client-e2e" ] || openssl rand -hex 32 > "$tmp_dir/secrets/client-e2e"
  [ -s "$tmp_dir/secrets/admin-password" ] || openssl rand -base64 24 > "$tmp_dir/secrets/admin-password"
  [ -s "$tmp_dir/secrets/master-key" ] || openssl rand -hex 32 > "$tmp_dir/secrets/master-key"
  printf '%s\n' 'http://mockfeishu:9090/hook' > "$tmp_dir/secrets/feishu-webhook"
  printf '%s\n' 'local-signing-secret' > "$tmp_dir/secrets/feishu-signing"
  chmod 750 "$tmp_dir" "$tmp_dir/secrets"
  chmod 640 "$tmp_dir/config.json" "$tmp_dir/secrets/"*
}

case "$action" in
  up)
    initialize
    compose up -d --build
    attempt=0
    until curl -fsS http://127.0.0.1:18081/readyz >/dev/null 2>&1; do
      attempt=$((attempt + 1))
      [ "$attempt" -lt 40 ] || { compose logs alertbridge; exit 1; }
      sleep 0.5
    done
    printf 'AlertBridge local demo is ready.\n'
    printf 'Admin URL: http://127.0.0.1:18081/admin/\n'
    printf 'Username: admin\n'
    printf 'Password: %s\n' "$(tr -d '\r\n' < "$tmp_dir/secrets/admin-password")"
    printf 'Send a test event: ./scripts/send-local-test.sh\n'
    ;;
  down)
    compose down --remove-orphans
    ;;
  reset)
    compose down -v --remove-orphans
    rm -rf "$tmp_dir"
    ;;
  status)
    compose ps
    ;;
  *)
    echo "usage: $0 {up|down|reset|status}" >&2
    exit 2
    ;;
esac
