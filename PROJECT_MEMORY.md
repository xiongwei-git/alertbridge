# AlertBridge Project Memory

## Current state (2026-07-15)

- The current implementation is a Docker-deployable notification gateway with a server-rendered admin console.
- Durable runtime: Go 1.26.5, pure-Go SQLite, one HTTP process plus one persistent delivery worker.
- Public API: the only supported inbound contract is `POST /api/v1/events`; health endpoints: `/healthz` and `/readyz`; admin console: `/admin/`.
- Implemented: per-client HMAC, timestamp/nonce replay protection, route authorization, rate limiting, idempotency, dedupe, recovery duration, silences, encrypted dynamic configuration, credential rotation, delivery inspection/retry, and persistent dead letters.
- Channel adapters: Feishu text/cards with optional per-channel security-keyword injection, Telegram Bot, ntfy, and TLS/STARTTLS SMTP.
- Terminal event records have a configurable retention period (default 30 days); pending work is never pruned.
- Deployment target: Baota Nginx terminates HTTPS and proxies to `127.0.0.1:18080`; the app container must not be directly public.
- Production secrets are files under `secrets/`, mounted read-only at `/run/secrets`; never store secret values in this file.
- `secrets/master-key` encrypts dynamic configuration and must be backed up with the SQLite volume.

## Accepted decisions

- Keep v1 single-process and single-instance. Do not add Redis/PostgreSQL until measured scale or multi-instance requirements justify them.
- Prefer a native channel adapter for Feishu. Its signature and response semantics are covered by tests.
- A Feishu channel may store one security keyword. The sender injects it into every text body or card title; Feishu error `19024` is permanent and must not enter the retry loop.
- Keep SQLite at WAL + FULL synchronous with one database connection for predictable single-node reliability.
- The production image is `scratch`, non-root, read-only, capability-free, and resource-limited.
- Do not add a second Caddy TLS layer under Baota.
- Keep the admin UI server-rendered with no external runtime/CDN. Dynamic config uses AES-256-GCM and atomic immutable snapshots.
- Keep reference fields safe to edit: client routes and route target channels use server-rendered checkbox choices, silence routes use a select, and technical terms expose hover/focus help without JavaScript.
- The admin integration guide must never decrypt saved client secrets. It derives the current base URL from a strictly validated request Host plus direct TLS or Baota's `X-Forwarded-Proto`, renders client IDs, and chooses a working route/severity from the live routing matrix, while examples keep the secret as a user-supplied placeholder.
- Keep one canonical, versioned inbound event API. Callers adapt their native payloads to AlertBridge; the gateway does not maintain product-named input parsers or heuristic field conversion. Removed `/hooks/*` paths return a structured `410 Gone` migration response only.

## Deferred scope

- Acknowledgement/escalation policies, Prometheus metrics, and multi-instance storage.

## Verification commands

```sh
docker run --rm -v "$PWD:/src" -w /src golang:1.26.5-alpine3.24 sh -c 'go test ./... && go vet ./...'
./test/e2e/run.sh
```
