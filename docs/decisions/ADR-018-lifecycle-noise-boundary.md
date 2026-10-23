# ADR-018: Lifecycle noise and recovery evidence

Accepted — 2026-10-23. Deployed to production in v0.6.3 at 03:32 UTC after offline and release verification.

Incidents remain keyed by client, route and explicit dedupe_key. Sources must give each independent check its own stable key. Keep the first firing immediate, permit severity escalation above the active incident's highest queued firing severity through the dedupe window, and preserve existing route/silence decisions. Older producer timestamps are recorded but cannot change state or create deliveries. Equal timestamps retain receipt order. Existing retained events provide the evidence, so history pruning bounds these comparisons; no new schema or history replay is introduced.

The consumer Bruce already owns a fixed ten-minute instability summary and ordinary info/test presentation policy. AlertBridge must not layer a second recovery timer over it or change other channels' info/test semantics. Transition-only events cannot establish consecutive successful checks. Sources must implement that evidence before emitting resolved; silence and elapsed time are never successful samples. A newer explicit resolved still closes an incident immediately.

This patch applies escalation and stale-event behavior to all lifecycle destinations, including MCP subscriptions, but preserves raw payloads, event identity, subscriptions, filters, enabled state and delivery retry behavior. Production configuration and source health checks require separate verification. Deployment needs an application replacement/restart, with rollback to the previous image; no dedicated database migration is required.
