#!/bin/sh
set -eu

project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp_dir="$project_dir/test/e2e/tmp"
password_file="$tmp_dir/admin-password"
action=${1:-up}

initialize() {
  umask 077
  mkdir -p "$tmp_dir"
  chmod 700 "$tmp_dir"
  if [ ! -s "$password_file" ]; then
    openssl rand -base64 24 > "$password_file"
  fi
  chmod 604 "$password_file"
}

compose() {
  ALERTBRIDGE_ADMIN_PASSWORD_FILE="$password_file" \
  ALERTBRIDGE_PORT=18081 \
  COMPOSE_PROJECT_NAME=alertbridge-local-demo \
  docker compose -f "$project_dir/compose.yaml" -f "$project_dir/compose.build.yaml" "$@"
}

case "$action" in
  up)
    initialize
    compose up -d --build
    attempt=0
    until curl -fsS http://127.0.0.1:18081/readyz >/dev/null 2>&1; do
      attempt=$((attempt + 1))
      [ "$attempt" -lt 60 ] || { compose logs alertbridge; exit 1; }
      sleep 0.5
    done
    printf 'AlertBridge local demo is ready.\n'
    printf 'Admin URL: http://127.0.0.1:18081/admin/\n'
    printf 'Username: admin\n'
    printf 'Password: %s\n' "$(tr -d '\r\n' < "$password_file")"
    printf 'Configure channels, routes, and clients in the admin console.\n'
    ;;
  down)
    initialize
    compose down --remove-orphans
    ;;
  reset)
    initialize
    compose down -v --remove-orphans
    rm -rf "$tmp_dir"
    ;;
  status)
    initialize
    compose ps
    ;;
  *)
    echo "usage: $0 {up|down|reset|status}" >&2
    exit 2
    ;;
esac
