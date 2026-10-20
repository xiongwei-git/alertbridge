# Docker and Baota deployment

AlertBridge is a single-instance service. Do not run multiple containers against the same SQLite volume. The public introduction at `/` is embedded in the image; all existing API and administrator authentication remains separate.

## Requirements and image access

Use Docker Engine with Compose v2, curl, OpenSSL, an NTP-synchronized Linux host and a TLS reverse proxy (Baota Nginx or equivalent). No Go, Git or source checkout is required.

ACR is the only image publication source:

```text
crpi-2q1p9uwje81xbddu.cn-shanghai.personal.cr.aliyuncs.com/ted_docker_mirror/ted
```

This repository requires authorized pull credentials. An anonymous manifest request for v0.5.1 returned HTTP 401 on 2026-10-17. Public source code does not provide anonymous registry access. Obtain an authorized registry account before deployment, then enter the password interactively:

```sh
docker login crpi-2q1p9uwje81xbddu.cn-shanghai.personal.cr.aliyuncs.com -u '<authorized-registry-user>'
```

Do not put registry passwords in command arguments, `.env`, service environment variables or source control. Use Docker's credential helper where available. The public endpoint is used by GitHub-hosted CI and general-purpose hosts.

## Minimal installation

Download the matching v0.6.1 Compose file. The public ACR endpoint is the default. Historical release files remain immutable; older files that default to GHCR require an explicit ACR image override.

```sh
mkdir -p /www/wwwroot/alertbridge
cd /www/wwwroot/alertbridge
curl -fsSLo compose.yaml \
  https://raw.githubusercontent.com/xiongwei-git/alertbridge/v0.6.1/compose.yaml
umask 077
mkdir -p secrets
chmod 700 secrets
printf '%s\n' \
  'ALERTBRIDGE_IMAGE=crpi-2q1p9uwje81xbddu.cn-shanghai.personal.cr.aliyuncs.com/ted_docker_mirror/ted' \
  'ALERTBRIDGE_IMAGE_TAG=v0.6.1' \
  'ALERTBRIDGE_PORT=18080' \
  'ALERTBRIDGE_ADMIN_USERNAME=admin' \
  'ALERTBRIDGE_DISPLAY_TIMEZONE=Asia/Shanghai' > .env
ADMIN_PASSWORD=$(openssl rand -base64 24 | tr -d '\n')
printf '%s\n' "$ADMIN_PASSWORD" > secrets/admin_password
chmod 644 secrets/admin_password
printf 'Save the administrator password now: %s\n' "$ADMIN_PASSWORD"
unset ADMIN_PASSWORD
chmod 600 .env
docker compose config
docker compose pull
docker compose up -d
docker compose ps
curl -fsS http://127.0.0.1:18080/readyz
```

Use a new deployment directory; do not overwrite an existing installation's `.env` or bootstrap password. Keep the `secrets` directory at `0700`. The file is `0644` so non-root UID 10001 can read the read-only Compose Secret mount; this is safe only while its parent directory is private. Only the password hash is stored in SQLite. Changing this bootstrap file does not reset an existing administrator password.

Configure Baota's HTTPS reverse proxy to `http://127.0.0.1:18080`, preserving the Host and forwarded HTTPS headers. Do not expose port 18080 publicly. See the [Nginx configuration](deployment.md#6-宝塔-https-反向代理) for proxy and OAuth discovery details. Optional administrator OIDC needs the matching `compose.oidc.yaml` overlay and a private server-side configuration Secret; keep that overlay active for every Compose operation.

## Optional VPC endpoint

Only same-region Alibaba ECS hosts with access to this instance's VPC endpoint should replace the existing `ALERTBRIDGE_IMAGE` line in `.env` with:

```dotenv
ALERTBRIDGE_IMAGE=crpi-2q1p9uwje81xbddu-vpc.cn-shanghai.personal.cr.aliyuncs.com/ted_docker_mirror/ted
```

Do not append duplicate keys. Log in to that endpoint separately:

```sh
docker login crpi-2q1p9uwje81xbddu-vpc.cn-shanghai.personal.cr.aliyuncs.com -u '<authorized-registry-user>'
```

The VPC address is not a general default and must not be used by GitHub-hosted CI, ordinary VPS hosts or local computers. `ALERTBRIDGE_IMAGE` remains overridable for appropriate deployments.

## Upgrade, rollback and recovery

Production pins a full published `vX.Y.Z`, never a floating tag. To upgrade, edit only `ALERTBRIDGE_IMAGE_TAG` to an existing version, then run `docker compose pull && docker compose up -d` and check readiness, the administrator UI and your actual integration. Roll back by selecting the previous compatible version using the same commands. Preserve any OIDC overlay and private configuration. Never run `docker compose down -v` on a production or persistent demo stack.

Back up and restore `alertbridge-data` and `alertbridge-secrets` together; an initialized database must never receive a silently regenerated master key. Preserve `.env`, bootstrap Secret and optional OIDC configuration separately with restricted permissions. See the [backup and restore procedures](deployment.md) for consistent SQLite backup and recovery commands.

Container security remains non-root, read-only, capability-free, loopback-bound and resource-limited. APIs and storage keep UTC semantics; `ALERTBRIDGE_DISPLAY_TIMEZONE` controls display only.

## Release evidence and troubleshooting

Future stable version tags run tests, vet, release checks and isolated Docker E2E before one AMD64/ARM64 build uploads directly to ACR. Tags remain `vX.Y.Z`, `vX.Y`, `latest`, and `vX` for non-zero major versions. GHCR publishing and GHCR-to-ACR copying are removed; historical GHCR images remain untouched.

This Personal Edition repository previously rejected BuildKit attestation manifests. SBOM and maximum provenance remain enabled in the same OCI build. Release validation checks blob integrity, both platforms and evidence subject digests before upload; each ACR tag must match the validated runtime index. GitHub retains signed evidence files as `release-evidence-vX.Y.Z` Actions artifacts for 90 days. Archive them for longer retention. GitHub attestations also bind the ACR image digest to the workflow without writing attestations to ACR. See [ADR-014](decisions/ADR-014-acr-only-image-publishing.md) for verification commands and tradeoffs.

For pull failures, check login to the exact endpoint, repository permission, the full published version and network reachability. A VPC endpoint requires suitable ECS network access. New-release remote write permission and GitHub signed attestations are verified only during the next authorized formal release, not by this configuration migration.
