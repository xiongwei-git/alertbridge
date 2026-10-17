# AlertBridge

**One small, self-hosted bridge between your services, your notification channels, and your agents.**

AlertBridge receives authenticated events, routes them to Feishu, Telegram, ntfy or TLS SMTP, and records delivery attempts in SQLite. Go, one HTTP process, one database connection and one persistent worker. No Redis, Node.js runtime or external CDN for the admin console.

[中文](README.md) · [Interactive demo source](site/index.html) · [Deployment](docs/deployment.md) · [API](docs/api.md) · [MCP / Dots](docs/mcp.md)

## Why use it?

- Keep channel credentials and retry logic out of every application.
- Track `firing` / `resolved` incidents and deduplicate repeated events.
- Inspect queued, sent and failed deliveries; acceptance is not delivery confirmation.
- Give agents read-only access to permitted routes through MCP, with OAuth + PKCE for Dots.

Use it for personal infrastructure, small teams and single-node operations. It is not a monitoring collector, highly available paging platform, escalation system or autonomous remediation service.

## Try locally

```sh
git clone https://github.com/xiongwei-git/alertbridge.git
cd alertbridge
./scripts/local-demo.sh up
```

Requires Docker Compose, OpenSSL and curl. The script prints the generated admin password and opens a local-only HTTP environment at `http://127.0.0.1:18081/admin/`. Create a channel, route and client in the console. Never expose this local environment publicly.

For a browser-only simulation, serve `site/` with `python3 -m http.server 18083 --directory site`. This demo uses fictional data and sends no notifications.

Production uses the published GHCR image, HTTPS at your reverse proxy, and loopback-only application exposure. Follow the [deployment guide](docs/deployment.md); back up the database and encryption-key volumes together. Detailed operating documentation is currently in Chinese.

## Integrate

- `POST /api/v1/notifications`: header-only Bearer token, server-bound route and severity, one-off `info` notifications.
- `POST /api/v1/events`: HMAC-SHA256, replay protection, idempotency and incident lifecycle. See [Python example](examples/python/README.md).
- `/mcp`: optional, read-only `list_alerts`, `get_alert`, `list_active_incidents`; no acknowledgement, mutation or automatic wake-up.

## Contribute

Read [CONTRIBUTING](CONTRIBUTING.md), [security policy](SECURITY.md), [code of conduct](CODE_OF_CONDUCT.md) and [decisions](docs/decisions/). Licensed under [Apache-2.0](LICENSE). Maintained on a best-effort basis; no availability or support SLA is promised.
