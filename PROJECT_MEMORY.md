# AlertBridge Project Memory

## Current state (2026-07-16)

- AlertBridge v0.2.1 is a lightweight Docker notification gateway with a server-rendered management console.
- Runtime: Go 1.26.5, pure-Go SQLite, one HTTP process, one SQLite connection, and one persistent delivery worker.
- Public API: `POST /api/v1/events`; health: `/healthz` and `/readyz`; admin: `/admin/`.
- Implemented: per-client HMAC, replay protection, route authorization, rate limiting, idempotency, dedupe, recovery duration, silences, credential rotation, delivery inspection/retry, and dead letters.
- Channel adapters: Feishu text/cards with optional security-keyword injection, Telegram Bot, ntfy, and TLS/STARTTLS SMTP.
- Production deployment needs `compose.yaml`, `.env`, and `secrets/admin_password`; it pulls `ghcr.io/xiongwei-git/alertbridge` and does not require a repository clone or JSON configuration.
- First startup accepts an operator-selected administrator password through a file-backed Compose Secret, stores only an Argon2id hash, and creates no business configuration. The host Secret directory is `0700`; environment-sourced Compose Secrets are not used because Docker Compose cannot materialize them into a read-only container rootfs.
- The AES-256-GCM master key is generated atomically with mode `0600` in the separate `alertbridge-secrets` volume. An initialized database must fail startup if that key is missing.
- `alertbridge-data` and `alertbridge-secrets` are an inseparable encrypted-backup recovery pair.
- Production topology: Baota Nginx terminates HTTPS and proxies to `127.0.0.1:18080`; the container port is never directly public.

## Accepted decisions

- Keep v1 single-process and single-instance; do not add Redis or PostgreSQL without measured need.
- Keep one canonical inbound event API. Callers adapt payloads; AlertBridge does not maintain product-named parsers.
- Keep SQLite at WAL + FULL synchronous with one connection.
- Keep the image `scratch`, non-root, read-only, capability-free, loopback-bound, and resource-limited.
- Publish official `linux/amd64` and `linux/arm64` images only through GitHub Actions and GHCR; Docker Hub is not used.
- Keep the admin UI server-rendered with no external JavaScript, font, or CDN dependency.
- Dynamic credentials use AES-256-GCM and atomic immutable runtime snapshots.
- Business configuration starts empty and is created only through the authenticated management console; there is no legacy JSON bootstrap path.
- Administrator login uses bounded Argon2id parameters and serialized verification; no fixed default password exists.

## Deferred scope

- Acknowledgement/escalation policies, Prometheus metrics, multi-instance storage, and administrator password self-service/reset flow.

## Verification commands

```sh
go test ./...
go vet ./...
./test/release/run.sh
./test/e2e/run.sh
```
