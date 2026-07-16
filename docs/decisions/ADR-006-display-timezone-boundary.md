# ADR-006: Convert time only at display boundaries

## Status

Accepted

## Date

2026-07-16

## Context

AlertBridge accepts RFC 3339 event times, stores Unix milliseconds in SQLite and reconstructs them as UTC. This is correct for ordering, deduplication, retries and incident duration, but the notification renderers and server-rendered admin console displayed those UTC values directly. A China deployment therefore showed a valid instant eight hours behind the operator's wall clock. Changing the VPS or container clock would not fix explicit UTC conversion in the application.

The production image is based on `scratch`, so it cannot rely on host zoneinfo files. The fix must remain deterministic across Docker hosts without changing the inbound API or persisted data.

## Decision

- API validation, SQLite storage, signing timestamps, retries and incident calculations continue to use UTC instants.
- `ALERTBRIDGE_DISPLAY_TIMEZONE` selects the presentation timezone and defaults to `Asia/Shanghai`.
- The binary embeds Go's timezone database so IANA locations work in the `scratch` image.
- The delivery worker converts event and incident timestamps immediately before passing an event to any notification channel. Shared renderers therefore produce consistent times for Feishu, Telegram, ntfy and SMTP.
- The admin console uses the same configured location for tables and local datetime form defaults.
- An invalid timezone prevents startup with a clear configuration error instead of silently falling back.

## Alternatives Considered

### Set `TZ` in Docker Compose

Rejected because the application explicitly reconstructed stored timestamps as UTC and a `scratch` image has no host timezone database. Process-global local time would also make behavior depend on deployment details.

### Store local timestamps or original offsets

Rejected because the absolute instant is the durable contract. Local storage complicates ordering, daylight-saving transitions and migration without improving alert semantics.

### Hard-code a fixed `+08:00` offset

Rejected because named IANA zones are clearer and allow deployments outside China, including regions with daylight-saving rules.

## Consequences

- Existing databases require no migration; upgrading the image changes presentation only.
- Existing v0.2.1 Compose deployments receive the `Asia/Shanghai` default after changing only the image tag. The v0.2.2 Compose file additionally exposes the optional override.
- The embedded timezone database slightly increases the static binary and image size, but adds no process, network dependency or runtime service.
- Notifications now include an explicit RFC 3339 offset such as `+08:00`, while internal logs may remain in UTC.
