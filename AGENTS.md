# AlertBridge repository guidance

## Scope and architecture

- AlertBridge is a lightweight, single-instance notification gateway built with Go and SQLite.
- `POST /api/v1/events` is the only supported inbound event contract. Callers adapt their payloads to it; do not add heuristic or product-named input parsers without a new ADR.
- Keep the runtime as one HTTP process, one SQLite connection, and one persistent delivery worker until measured requirements justify a different architecture.
- The admin UI is server-rendered and has no external JavaScript, font, or CDN dependency.
- Production TLS terminates at Baota Nginx. The application port remains bound to the host loopback interface.

## Security boundaries

- Never commit `config/config.json`, `.env`, anything under `secrets/`, backups, databases, Webhooks, tokens, passwords, client keys, or the dynamic configuration master key.
- Do not weaken HMAC authentication, replay protection, route authorization, secure cookies, TLS requirements, outbound host allowlists, or Secret file permission checks to make a test pass.
- `secrets/master-key` and the SQLite volume are a recovery pair and must be backed up together.
- Keep Docker non-root, read-only, capability-free, resource-limited, and loopback-bound.

## Verification

Run focused tests while changing code, then complete:

```sh
go test ./...
go vet ./...
./test/e2e/run.sh
```

The E2E script uses a separate Compose project, ports `18082`/`19091`, and a temporary directory by default. The local demo uses `18081`/`19090` and persistent ignored files under `test/e2e/tmp`.

Do not run `docker compose down -v` against the production stack or the user's local demo unless data deletion is explicitly requested.

## Documentation

- Update `README.md` for user-visible setup or capability changes.
- Update `docs/deployment.md` for Docker, Baota, backup, restore, upgrade, or operational changes.
- Update `docs/api.md` and `docs/openapi.yaml` together when the public API changes.
- Record durable architecture or security tradeoffs under `docs/decisions/`.
