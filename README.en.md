# AlertBridge

AlertBridge is a single-instance notification gateway written in Go with SQLite. It accepts authenticated HTTP events, routes them to Feishu, Telegram, ntfy or TLS SMTP, and records events, incidents and delivery attempts.

[中文](README.md) · [Website and simulated demo](https://opensource.tedxiong.com/alertbridge/) · [Deployment](docs/deployment.md) · [API](docs/api.md) · [MCP / Dots](docs/mcp.md)

Current release: **v0.5.1**. The public introduction at `/` is embedded in the service and Docker image, with no separate static Nginx setup. Optional administrator OIDC, notification APIs, MCP authorization and business data formats remain unchanged.

## Features

- HMAC-SHA256 event API with replay protection, event ID idempotency and `firing` / `resolved` incident state.
- Header-only Bearer notification API with server-bound source, route, severity and rate limit.
- Routing, deduplication, silence windows, a persistent SQLite queue, retries and dead-letter requeue.
- Server-rendered admin console with password login and optional OIDC Authorization Code + PKCE S256. Administrator access is bound to an explicit issuer and subject; usernames and email addresses do not grant access.
- Optional read-only MCP: `list_alerts`, `get_alert`, `list_active_incidents`. Dots connects through OAuth; clients supporting custom headers may use an independent static Bearer.

One HTTP process, one SQLite connection and one persistent worker. No Redis, Node.js runtime or external CDN for the admin console. The container runs as UID 10001 with a read-only root filesystem and no Linux capabilities.

Use it for personal infrastructure, small teams and single-node operations. Multi-instance HA, alert aggregation, on-call scheduling, escalation, acknowledgement, automatic remediation and third-party inbound parsers are not implemented.

## Try locally

```sh
git clone https://github.com/xiongwei-git/alertbridge.git
cd alertbridge
./scripts/local-demo.sh up
```

Requires Docker Compose, OpenSSL and curl. Open `http://127.0.0.1:18081/admin/` with the generated password. Configure a channel, route and client. This HTTP environment is local-only.

The [browser demo](https://opensource.tedxiong.com/alertbridge/#demo) uses fictional data, sends no requests to a gateway, and does not test real authentication or delivery. To preview its source, run `./scripts/build-site.sh` then `python3 -m http.server 18083 --bind 127.0.0.1 --directory dist-site`.

## Deploy

Use the official `ghcr.io/xiongwei-git/alertbridge:v0.5.1` image and matching versioned Compose files. Follow the [deployment guide](docs/deployment.md) for file-backed bootstrap credentials and HTTPS reverse proxy setup. Bind the application to loopback only. Back up the database and master-key volumes together.

OIDC is optional and has been tested with Tinyauth. It requires the versioned `compose.oidc.yaml` overlay and a restricted server-side JSON Secret. Keep the overlay active for all Compose operations, and back up its configuration separately. Administrator OIDC and MCP OAuth are separate flows: provider tokens are not issued to agents. Logout clears only the local application session.

Detailed operational documentation is currently in Chinese.

## Interfaces

| Endpoint | Authentication | Purpose |
| --- | --- | --- |
| `POST /api/v1/notifications` | Constrained Bearer | One-off `info` notifications; HTTP 200 |
| `POST /api/v1/events` | HMAC | Idempotency, replay protection and incident lifecycle; HTTP 202 |
| `/mcp` | Independent OAuth or static Bearer | Route-scoped read-only queries |

A successful notification response is not delivery confirmation. Inspect `outcome` and delivery records. See the [Python HMAC example](examples/python/README.md). For the ten latest MCP events, use `list_alerts` with `{"order":"desc","limit":10}`.

## Contribute

Read [CONTRIBUTING](CONTRIBUTING.md), [security policy](SECURITY.md), [code of conduct](CODE_OF_CONDUCT.md) and [decisions](docs/decisions/). Licensed under [Apache-2.0](LICENSE). Maintained on a best-effort basis without an availability or support SLA.
