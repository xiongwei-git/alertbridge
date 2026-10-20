# MCP 2.0 independent acceptance

The same authenticated `/mcp` supports queries and optional `alert.created` subscriptions. It implements revision **2026-07-28** directly: no initialize handshake, SDK-era bridge, or protocol-header rewriting.

## Reproduce locally

```sh
./test/mcp2/run.sh
```

This starts only the dedicated local `alertbridge-mcp-sandbox` Compose project on loopback port 18083, with separate recovery volumes, credentials and a synthetic notification sink. It does not use the user's demo project or any production files. `python3 scripts/mcp-events-sandbox.py down` stops it while preserving both volumes. Runtime files, credentials and evidence are ignored under `test/mcp2/tmp/`.

Validation has three independent layers:

1. **Official TypeScript Client 2.3.1**: pin `2026-07-28`, connect/discover, list tools, invoke all three query tools, validate their structured outputs, assert no `initialize` or version fallback. The wire capture confirms `events` capability; the core client's parser strips unknown draft capability keys, so its parsed capability object cannot validate this OpenAI-specific draft.
2. **Official JSON Schema**: `fixtures/source.json` pins the upstream commit and content SHA-256; `fixtures/LICENSE` preserves the upstream license. Python jsonschema 4.25.1 validates actual response envelopes and discovery/list/call result schemas. Independent HTTP cases cover missing metadata, capabilities and mirrored headers; mismatches; unsupported version; removed initialize; and unsafe callback rejection. The script imports no server helpers.
3. **Go Events/worker integration**: trusted local TLS receiver with independent Standard Webhooks signature verification; subscription/filtering, retries across DB reopen, renewal, cancellation, secret rotation, revocation, generation isolation, and callback SSRF/redirect restrictions. Protocol fixtures carry actual MCP 2.0 metadata, rather than only changing a version header.

The upstream schema is fetched once and checked in. Do not update its digest just to pass a test; review an upstream protocol change first. Development dependencies are installed only in the ignored test folder and are not application runtime dependencies.

## Real Dots acceptance

The isolated Baota test URL is `https://test.tedxiong.com/mcp`. It is not the production notification service. Use OAuth public client `alertbridge-dots`, no client secret, `alerts:read`. Test credentials exist only in the test server's private directory; never paste them into chat or commit them.

Rescan the test plugin, confirm both tools and `alert.created`, and create an explicit monitoring instruction in Dots. Use only the `sandbox` route and synthetic alerts. The operator must confirm and add the exact actual platform callback hostname to the test allowlist before successful subscription. No wildcard or arbitrary-host bypass is permitted.

Acceptance checkpoints: actual subscribe request; signed challenge echo; persisted subscription; independently observed webhook 2xx; Dots response to the synthetic ID; nonmatching-event filtering; cancellation; no response to a subsequent event. No current automated result establishes these account-level checkpoints. A 2xx is receipt, not Agent completion.

The deployed production baseline is included in v0.6.0. These tests do not establish successful real production Dots execution; that requires a separately observed response.
