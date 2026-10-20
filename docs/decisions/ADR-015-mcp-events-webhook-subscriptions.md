# ADR-015: MCP Events webhook subscriptions in the existing process

## Status

Accepted — 2026-10-17. Native MCP 2.0 implementation; isolated test deployment at test.tedxiong.com on 2026-10-18. Production upgrade and real Dots acceptance remain separate work.

## Context

Read-only MCP tools require a caller. Users want new alerts to trigger a subscribed Dots/Work chat without Agent polling. OpenAI documents webhook subscriptions using MCP 2.0 discovery (`2026-07-28`) and the draft Events contract. The initial version-header adapter did not implement required MCP 2.0 metadata or result semantics. It has been removed. Query and notification capabilities must work together under one consistent protocol, without maintaining two SDK eras.

## Decision

- Use one native MCP 2.0 endpoint for queries and optional Events: `server/discover`, three query tools, and `events/list/subscribe/unsubscribe`. Remove the old Go MCP SDK and all version-header rewriting. MCP 2.0 uses per-request metadata, not an initialize handshake. Validate required metadata and HTTP mirrors; return normative version/header errors, complete results, private cache fields and server identity. Optional features not advertised by this application remain unsupported.
- Expose only `alert.created`, with optional exact `routing_key`, `source`, `severity`, `status` filters. Omitting a route means the configured authorized routes, never all routes. Push contains the internal record ID, source, route, severity, status, and title; detail stays in `get_alert`.
- Emit only newly accepted queued events. Do not wake on duplicate intake, silence, incident dedupe, or orphan recovery. This supplements channel delivery; public ingress `deliveries` still counts configured notification channels only. It neither acknowledges nor resolves incidents.
- Reuse the process, one SQLite connection and existing persistent delivery worker. Insert matching callback jobs atomically with the event. Use an internal `@mcp-event:` namespace that cannot collide with configurable channel IDs. Snapshot subscription generation per job so unsubscribe/expiration followed by recreation cannot revive old deliveries.
- Bind subscriptions to the static credential/policy or OAuth grant/policy. Recheck authorization, expiration, generation and cancellation before each attempt; OAuth access token refresh does not terminate an otherwise live grant. Revocation stops queued deliveries. Already in-flight HTTPS requests cannot be recalled.
- Reuse master-key encryption for callback URLs and current/previous signing secrets. No plaintext callback URLs or signing keys in logs, administrative views, tool results or configuration. Database and key volumes remain a recovery pair.
- Require an operator-supplied exact callback host allowlist, HTTPS/443, no proxy or redirects, and public address validation at every connection. Resolve once per connection and dial the validated IP while TLS verifies the original hostname. Reject private, special-use and transition ranges, including IPv4-mapped addresses. There is no insecure development bypass.
- Verify a signed single-use random callback challenge before activation. Cache successful unchanged-secret verification for five minutes for the same principal, callback URL and signing secret. Subscription operations are serialized within the single process. Sign Standard Webhooks HMAC-SHA256 over the exact serialized bytes; retain event IDs across bounded retries and generate a fresh signing timestamp each time.
- Default/max lifetime is 24 hours; honor shorter positive `ttlMs`. A null request still receives a finite lifetime. No replay (`cursor: null`, `truncated: false`); only jobs already persisted for a live subscription survive restarts. Do not claim recovery for events outside an active subscription. Refresh retains identity and generation. Secret rotation dual-signs for five minutes. Limit 100 subscriptions per principal and 1,000 total. Hourly cleanup removes expired/revoked credentials.
- Retry network failures, 408/425/429 and 5xx through the existing worker policy. Never retry 410/413; 410 also cancels the subscription. A 2xx means callback acceptance, not Agent completion.
- `alerts:read` remains the scope: subscriptions read future data in the same authorized routes, without adding source-app write tools. OAuth consent explains the optional callback behavior when Events is enabled.

## Alternatives Considered

- Agent polling: retained as a separate fallback but does not meet event-driven triggering.
- SSE/streaming: not supported by the documented ChatGPT Events integration.
- Separate server, queue, worker or Redis: unnecessary for the current single-instance requirement.
- Broad callback acceptance: conflicts with outbound allowlists and increases SSRF exposure.
- Pretending a tool named `subscribe` is an Events implementation: does not satisfy discovery or protocol subscription methods.

## Consequences

The implementation and draft Events contract must be revisited as the specification evolves. Official TypeScript Client 2.3.1 pinned to 2026-07-28 and the official schema pinned to commit 86f6e0c675afea9a8f233e09b6faf566b126086c independently verify the implemented query/wire surface. A draft Events capability is checked on the wire because the core client strips unknown draft capability keys. Local protocol, database, worker and callback tests cannot prove real-account support, discovery caching, batching, or Agent response. Production requires checking actual callback hostnames, rescanning the plugin and explicitly creating a monitoring task, then verifying receipt and Agent behavior independently.

Source: [OpenAI MCP Events](https://developers.openai.com/plugins/build/mcp-events), checked 2026-10-17.


Protocol authority: [MCP 2.0 base](https://modelcontextprotocol.io/specification/2026-07-28/basic), [versioning](https://modelcontextprotocol.io/specification/2026-07-28/basic/versioning), [HTTP binding](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http). These correct the earlier mistaken assumption that 2026-07-28 still needed `initialize`. Required metadata, mirrors, `resultType`, and cache fields are verified independently; this is not a certification of unadvertised optional features or of the Dots account workflow.
