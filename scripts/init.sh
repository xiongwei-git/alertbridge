#!/bin/sh
set -eu

umask 077
project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
mkdir -p "$project_dir/config" "$project_dir/secrets"

if [ ! -f "$project_dir/config/config.json" ]; then
  cp "$project_dir/config/config.example.json" "$project_dir/config/config.json"
fi

if [ ! -f "$project_dir/secrets/client-monitoring" ]; then
  openssl rand -hex 32 > "$project_dir/secrets/client-monitoring"
fi

if [ ! -f "$project_dir/secrets/admin-password" ]; then
  openssl rand -base64 24 > "$project_dir/secrets/admin-password"
fi

if [ ! -f "$project_dir/secrets/master-key" ]; then
  openssl rand -hex 32 > "$project_dir/secrets/master-key"
fi

for secret in feishu-ops-webhook feishu-ops-signing-secret; do
  if [ ! -f "$project_dir/secrets/$secret" ]; then
    : > "$project_dir/secrets/$secret"
  fi
done

chmod 750 "$project_dir/config" "$project_dir/secrets"
chmod 640 "$project_dir/config/config.json" "$project_dir/secrets/"*

if [ ! -f "$project_dir/.env" ]; then
  printf 'ALERTBRIDGE_GID=%s\n' "$(id -g)" > "$project_dir/.env"
fi

cat <<'EOF'
Initialization complete.

Before starting AlertBridge:
1. Put the full Feishu webhook URL in secrets/feishu-ops-webhook.
2. Put the Feishu robot signing secret in secrets/feishu-ops-signing-secret.
3. The admin username is admin; retrieve its generated password from secrets/admin-password.
4. Review config/config.json.
5. Keep the generated ALERTBRIDGE_GID in .env aligned with the files' group.
EOF
