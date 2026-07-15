#!/bin/sh
set -eu

project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
ci_workflow="$project_dir/.github/workflows/ci.yml"
release_workflow="$project_dir/.github/workflows/release.yml"
e2e_script="$project_dir/test/e2e/run.sh"
prod_compose=$(mktemp)
build_compose=$(mktemp)
env_compose=$(mktemp)
env_project=$(mktemp -d)
secret_file="$env_project/secrets/admin_password"
trap 'rm -f "$prod_compose" "$build_compose" "$env_compose"; rm -rf "$env_project"' EXIT INT TERM

require_file() {
  [ -f "$1" ] || {
    printf 'missing required release file: %s\n' "$1" >&2
    exit 1
  }
}

require_match() {
  pattern=$1
  file=$2
  grep -Eq "$pattern" "$file" || {
    printf 'required pattern not found in %s: %s\n' "$file" "$pattern" >&2
    exit 1
  }
}

require_file "$ci_workflow"
require_file "$release_workflow"
require_file "$e2e_script"
require_file "$project_dir/compose.build.yaml"
require_file "$project_dir/docs/decisions/ADR-004-github-container-registry.md"
require_file "$project_dir/VERSION"
if ! grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' "$project_dir/VERSION"; then
  printf 'VERSION must contain one semantic version such as v0.2.0\n' >&2
  exit 1
fi

mkdir -p "$(dirname "$secret_file")"
chmod 700 "$(dirname "$secret_file")"
printf '%s\n' 'release-check-password' > "$secret_file"
chmod 604 "$secret_file"

ALERTBRIDGE_ADMIN_PASSWORD_FILE="$secret_file" ALERTBRIDGE_IMAGE_TAG=v9.8.7 docker compose -f "$project_dir/compose.yaml" config > "$prod_compose"
require_match 'image: ghcr\.io/xiongwei-git/alertbridge:v9\.8\.7' "$prod_compose"
require_match 'file: .*/secrets/admin_password' "$prod_compose"
require_match 'target: /run/secrets/admin_password' "$prod_compose"
require_match 'target: /var/lib/alertbridge-secrets' "$prod_compose"
if grep -Eq '^[[:space:]]+environment: ALERTBRIDGE_ADMIN_PASSWORD$' "$prod_compose"; then
  printf 'read-only production services require a file-backed Compose secret\n' >&2
  exit 1
fi
if grep -Eq '^[[:space:]]+build:' "$prod_compose"; then
  printf 'production Compose must pull a published image instead of building source\n' >&2
  exit 1
fi
if grep -Eq 'ALERTBRIDGE_CONFIG|config\.json|type: bind' "$prod_compose"; then
  printf 'production Compose must not depend on repository config or bind mounts\n' >&2
  exit 1
fi

cp "$project_dir/compose.yaml" "$env_project/compose.yaml"
printf '%s\n' \
  'ALERTBRIDGE_IMAGE_TAG=v9.8.7' \
  'ALERTBRIDGE_ADMIN_USERNAME=admin' > "$env_project/.env"
(cd "$env_project" && docker compose config) > "$env_compose"
require_match 'image: ghcr\.io/xiongwei-git/alertbridge:v9\.8\.7' "$env_compose"
if grep -Fq 'release-check-password' "$env_compose"; then
  printf 'Compose rendered the bootstrap password into service metadata\n' >&2
  exit 1
fi

ALERTBRIDGE_ADMIN_PASSWORD_FILE="$secret_file" ALERTBRIDGE_IMAGE_TAG=v9.8.7 ALERTBRIDGE_VERSION=test-build \
  docker compose -f "$project_dir/compose.yaml" -f "$project_dir/compose.build.yaml" config > "$build_compose"
require_match 'image: alertbridge:local' "$build_compose"
require_match '^[[:space:]]+build:' "$build_compose"
require_match 'VERSION: test-build' "$build_compose"

require_match 'pull_request:' "$ci_workflow"
require_match 'contents: read' "$ci_workflow"
require_match 'go test ./\.\.\.' "$ci_workflow"
require_match 'go vet ./\.\.\.' "$ci_workflow"
require_match '\./test/e2e/run\.sh' "$ci_workflow"

require_match "tags: \['v\*\.\*\.\*'\]" "$release_workflow"
require_match 'packages: write' "$release_workflow"
require_match 'attestations: write' "$release_workflow"
require_match 'id-token: write' "$release_workflow"
require_match 'REGISTRY: ghcr\.io' "$release_workflow"
require_match 'IMAGE_NAME: \$\{\{ github\.repository \}\}' "$release_workflow"
require_match 'linux/amd64,linux/arm64' "$release_workflow"
require_match 'type=semver,pattern=\{\{raw\}\}' "$release_workflow"
require_match 'type=semver,pattern=v\{\{major\}\}\.\{\{minor\}\}' "$release_workflow"
require_match "type=semver,pattern=v\\{\\{major\\}\\},enable=\\\$\\{\\{ !startsWith\\(github\\.ref, 'refs/tags/v0\\.'\\) \\}\\}" "$release_workflow"
require_match 'type=raw,value=latest' "$release_workflow"
require_match 'go test ./\.\.\.' "$release_workflow"
require_match 'go vet ./\.\.\.' "$release_workflow"
require_match '\./test/e2e/run\.sh' "$release_workflow"

# Docker Compose implements file-backed secrets as bind mounts on Linux. Keep
# the host directory private while allowing the non-root container to read the
# mounted file itself.
require_match 'chmod 700 "\$tmp_dir"' "$e2e_script"
require_match 'chmod 604 "\$password_file"' "$e2e_script"
require_match 'docker compose -f "\$project_dir/compose.yaml" -f "\$compose_file"' "$e2e_script"

if grep -R -n -E 'pull_request_target|docker\.io|index\.docker\.io' "$project_dir/.github/workflows"; then
  printf 'unsafe trigger or Docker Hub reference found in workflows\n' >&2
  exit 1
fi

uses_count=$(grep -R -h -E '^[[:space:]]*uses:' "$project_dir/.github/workflows" | wc -l | tr -d ' ')
[ "$uses_count" -gt 0 ] || {
  printf 'workflows contain no external actions\n' >&2
  exit 1
}
bad_uses=$(grep -R -h -E '^[[:space:]]*uses:' "$project_dir/.github/workflows" | grep -Ev '@[0-9a-f]{40}([[:space:]]*#.*)?$' || true)
if [ -n "$bad_uses" ]; then
  printf '%s\n' "$bad_uses" >&2
  printf 'all external actions must be pinned to a full commit SHA\n' >&2
  exit 1
fi

printf 'Release configuration checks passed: repository-free GHCR deployment, Compose secret bootstrap, isolated persistent key volume, pinned actions, least privilege, and multi-architecture publishing.\n'
