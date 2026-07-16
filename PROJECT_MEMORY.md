# AlertBridge Project Memory

## Current state (2026-07-16)

- AlertBridge v0.2.3 is a lightweight, single-instance notification gateway ready for its current production stage.
- Build toolchain: Go 1.26.5 with module language level 1.25.0. Runtime uses pure-Go SQLite, one HTTP process, one SQLite connection, and one persistent delivery worker.
- Public API: `POST /api/v1/events`; health: `/healthz` and `/readyz`; management console: `/admin/`.
- Implemented: per-client HMAC, replay protection, route authorization, rate limiting, idempotency, incident lifecycle through matching `firing` and `resolved` events, silences, credential rotation, persistent delivery retries, manual retry, and dead letters.
- Channel adapters: Feishu text/cards with optional security-keyword injection, Telegram Bot, ntfy, and TLS/STARTTLS SMTP.
- The admin UI is server-rendered and has no external JavaScript, font, or CDN dependency.
- Production deployment needs only `compose.yaml`, `.env`, and `secrets/admin_password`; it does not require a repository clone or JSON business configuration.
- Compose defaults to the official GHCR image. An authenticated ACR copy of the same GHCR digest can be selected through `ALERTBRIDGE_IMAGE` for faster pulls from mainland China.
- Current production topology: `https://notify.tedxiong.com` terminates TLS at Baota Nginx and proxies to the loopback-bound application port `127.0.0.1:18080`. The verified deployment runs v0.2.3 through the Shanghai ACR VPC endpoint.

## Accepted decisions

- Keep the current runtime single-process and single-instance; do not add Redis, PostgreSQL, or additional workers without measured need.
- Keep one canonical inbound event API. Callers adapt payloads; AlertBridge does not maintain product-named or heuristic input parsers.
- Keep SQLite at WAL + `synchronous=FULL` with one connection.
- Keep the image `scratch`, non-root, read-only, capability-free, loopback-bound, and resource-limited.
- GHCR is the only official release registry. ACR is an optional authenticated deployment mirror copied from the immutable GHCR digest; Docker Hub is not used.
- Dynamic channel credentials use AES-256-GCM and atomic immutable runtime snapshots.
- Business configuration starts empty and is created only through the authenticated management console; there is no JSON bootstrap path.
- Administrator login uses bounded Argon2id parameters and serialized verification; no fixed default password exists.
- Event timestamps are stored and transmitted as UTC; timezone conversion is presentation-only, with `Asia/Shanghai` as the deployment default.

## Operational boundaries

- `alertbridge-data` and `alertbridge-secrets` are an inseparable encrypted-backup recovery pair. Never delete or restore only one of them.
- First startup accepts the administrator bootstrap password through the file-backed `secrets/admin_password` Compose Secret and stores only its Argon2id hash.
- The host Secret directory must remain `0700`. Never move the administrator password into the service environment.
- An initialized database must fail startup if its encryption master key is missing; never silently regenerate the key.
- Production TLS terminates at Baota Nginx. Do not expose the application container port publicly.
- Release tags run tests, static analysis, Docker E2E, and multi-architecture GHCR publishing. The optional ACR job copies the published digest instead of rebuilding it.
- Production upgrades and rollbacks change only the pinned `ALERTBRIDGE_IMAGE_TAG`, then run `docker compose pull && docker compose up -d`.
- Do not run `docker compose down -v` against production or a persistent local demo unless data deletion is explicitly requested.

## Deferred scope

- Multi-event aggregation, acknowledgement, timeout escalation, alert detail pages, Prometheus metrics, multi-instance storage, administrator password self-service/reset, and additional outbound channel adapters.
- Product-named inbound parsers remain outside scope; source systems or trusted adapters must produce the canonical API payload and signature.

## Verification commands

```sh
go test ./...
go vet ./...
./test/release/run.sh
./test/e2e/run.sh
```

## Resume point

- The current milestone is complete and the project is in maintenance mode.
- Start a future task by reading `AGENTS.md`, `README.md`, this file, and only the relevant document under `docs/`.
- There is no required feature backlog for the next session. Prefer bug fixes, operational evidence, or measured user demand over speculative expansion.
