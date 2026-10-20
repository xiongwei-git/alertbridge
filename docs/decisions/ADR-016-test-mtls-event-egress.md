# ADR-016: Explicit mTLS CONNECT for isolated MCP Events tests

Status: accepted for the isolated test deployment, 2026-10-18.

The existing callback transport ignores generic proxy environment variables and
validates/binds locally resolved public IPs. Baota's callback DNS/TLS path cannot
reach the required endpoint. An explicit private file config selects a dedicated
mTLS CONNECT transport. The authenticated gateway now owns target DNS validation
(all answers public, literal-IP dial), with client-certificate fingerprint and
exact-host/443 ACLs. This is an explicit trust-boundary change, not an ordinary
HTTPS_PROXY option. The application retains callback host policy, HTTPS URL policy,
original-host TLS validation, signature generation, redirect rejection and queue.

Each node generates its own key. Only CSRs and public certificates are exchanged.
No credentials enter the image. Configuration/key permissions are checked. Startup
fails on invalid configuration; configured gateway failure cannot fall back to
direct access. The default transport remains unchanged when not configured.

Production image/configuration/container are out of scope. Test acceptance must
separate CONNECT/TLS success, signed webhook receipt and actual Dots execution.

2026-10-19: isolated test transport reloads client credentials per new connection
from one resolved generation directory. EgressHub controls a timed overlap window;
node-local renewal switches the directory atomically and rolls back on failed
validation. Application signatures, destination policy, queue, redirects and inner
TLS validation remain unchanged. Production is not migrated.
