#!/bin/sh
set -eu

project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
ci_workflow="$project_dir/.github/workflows/ci.yml"
release_workflow="$project_dir/.github/workflows/release.yml"
prod_compose=$(mktemp)
build_compose=$(mktemp)
init_project=$(mktemp -d)
trap 'rm -f "$prod_compose" "$build_compose"; rm -rf "$init_project"' EXIT INT TERM

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
require_file "$project_dir/compose.build.yaml"
require_file "$project_dir/docs/decisions/ADR-004-github-container-registry.md"
require_file "$project_dir/VERSION"
if ! grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' "$project_dir/VERSION"; then
  printf 'VERSION must contain one semantic version such as v0.1.0\n' >&2
  exit 1
fi

ALERTBRIDGE_IMAGE_TAG=v9.8.7 docker compose -f "$project_dir/compose.yaml" config > "$prod_compose"
require_match 'image: ghcr\.io/xiongwei-git/alertbridge:v9\.8\.7' "$prod_compose"
if grep -Eq '^[[:space:]]+build:' "$prod_compose"; then
  printf 'production Compose must pull a published image instead of building source\n' >&2
  exit 1
fi

ALERTBRIDGE_IMAGE_TAG=v9.8.7 ALERTBRIDGE_VERSION=test-build \
  docker compose -f "$project_dir/compose.yaml" -f "$project_dir/compose.build.yaml" config > "$build_compose"
require_match 'image: alertbridge:local' "$build_compose"
require_match '^[[:space:]]+build:' "$build_compose"
require_match 'VERSION: test-build' "$build_compose"

mkdir -p "$init_project/config" "$init_project/scripts"
cp "$project_dir/VERSION" "$init_project/VERSION"
cp "$project_dir/config/config.example.json" "$init_project/config/config.example.json"
cp "$project_dir/scripts/init.sh" "$init_project/scripts/init.sh"
chmod +x "$init_project/scripts/init.sh"
"$init_project/scripts/init.sh" >/dev/null
require_match '^ALERTBRIDGE_GID=[0-9]+$' "$init_project/.env"
require_match '^ALERTBRIDGE_IMAGE_TAG=v[0-9]+\.[0-9]+\.[0-9]+$' "$init_project/.env"
"$init_project/scripts/init.sh" >/dev/null
[ "$(grep -c '^ALERTBRIDGE_IMAGE_TAG=' "$init_project/.env")" -eq 1 ] || {
  printf 'init.sh must not duplicate ALERTBRIDGE_IMAGE_TAG\n' >&2
  exit 1
}

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

printf 'Release configuration checks passed: GHCR-only production image, pinned actions, least privilege, tests, version tags, and multi-architecture publishing.\n'
