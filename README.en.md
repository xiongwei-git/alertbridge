# AlertBridge

AlertBridge is a single-instance notification gateway written in Go with SQLite. It accepts authenticated HTTP events, routes them to Feishu, Telegram, ntfy or TLS SMTP, and records events, incidents and delivery attempts.

[中文](README.md) · [Website and simulated demo](https://opensource.tedxiong.com/alertbridge/) · [Deployment](docs/deployment.en.md) · [API](docs/api.md) · [MCP / Dots](docs/mcp.md)

Current release: **v0.6.0**. Existing channels can be edited from the admin console. This release includes the deployed MCP 2.0, optional Events and EgressHub SDK baseline. Notification APIs and administrator login remain compatible. MCP requires revision `2026-07-28`; the legacy initialize handshake is no longer supported.

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

ACR is the only image publication source: `crpi-2q1p9uwje81xbddu.cn-shanghai.personal.cr.aliyuncs.com/ted_docker_mirror/ted`. Both AMD64 and ARM64 are built together. Pulls require an authorized registry account and `docker login`; anonymous pulls are not available. Existing v0.5.1 is already present in ACR. Use matching versioned Compose files and explicitly set `ALERTBRIDGE_IMAGE` for older Compose files that still default to GHCR. Follow the [English deployment guide](docs/deployment.en.md) for login, public versus VPC endpoints, file-backed bootstrap credentials and HTTPS setup. Bind the application to loopback only. Back up the database and master-key volumes together.

Future version tags publish directly to ACR, with no GHCR publication or registry-to-registry copy. Historical GHCR images are retained. Validated per-platform SBOM/provenance and signed workflow attestations remain on GitHub; see [ADR-014](docs/decisions/ADR-014-acr-only-image-publishing.md). This workflow change does not create a new release.

OIDC is optional and has been tested with Tinyauth. It requires the versioned `compose.oidc.yaml` overlay and a restricted server-side JSON Secret. Keep the overlay active for all Compose operations, and back up its configuration separately. Administrator OIDC and MCP OAuth are separate flows: provider tokens are not issued to agents. Logout clears only the local application session.

The [Chinese operations guide](docs/deployment.md) covers additional backup, recovery and troubleshooting detail.

## Interfaces

| Endpoint | Authentication | Purpose |
| --- | --- | --- |
| `POST /api/v1/notifications` | Constrained Bearer | One-off `info` notifications; HTTP 200 |
| `POST /api/v1/events` | HMAC | Idempotency, replay protection and incident lifecycle; HTTP 202 |
| `/mcp` | Independent OAuth or static Bearer | Route-scoped read-only queries |

A successful notification response is not delivery confirmation. Inspect `outcome` and delivery records. See the [Python HMAC example](examples/python/README.md). For the ten latest MCP events, use `list_alerts` with `{"order":"desc","limit":10}`.

## Contribute

Read [CONTRIBUTING](CONTRIBUTING.md), [security policy](SECURITY.md), [code of conduct](CODE_OF_CONDUCT.md) and [decisions](docs/decisions/). Licensed under [Apache-2.0](LICENSE). Maintained on a best-effort basis without an availability or support SLA.


Optional MCP Events adds `alert.created` webhook subscriptions without Agent polling. It is disabled by default; enable with `ALERTBRIDGE_MCP_EVENTS=1` and an exact `ALERTBRIDGE_MCP_EVENT_CALLBACK_HOSTS` allowlist. One authenticated MCP 2.0 endpoint supports both query tools and Events, with per-request metadata and matching HTTP mirrors; no legacy SDK or version-header rewriting is used. Subscription credentials are encrypted; queued callbacks reuse the single worker, support bounded retries, and stop on cancellation, expiration or revoked authorization. Independent official-client/schema checks pass, and an isolated synthetic test instance is deployed; the production baseline uses the SDK, while real production Dots execution remains unverified. See [MCP guide](docs/mcp.md#主动触发) and [ADR-015](docs/decisions/ADR-015-mcp-events-webhook-subscriptions.md).
