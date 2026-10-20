# ADR-017: Shared EgressHub SDK for MCP Events callbacks

Accepted 2026-10-19. Supersedes ADR-016's inline transport and test-only rollout scope after explicit owner approval.

The inline callback transport duplicated EgressHub's CONNECT, TLS, destination policy and atomic credential loading.
Import the shared `egresshub/client` package and keep only application callback allowlist subset validation in the adapter.
AlertBridge retains subscription authorization, signatures, delivery queue and retries; ordinary notification channels retain their existing transport.
There is no generic proxy environment switch, plaintext tunnel, TLS interception, direct fallback or SDK business retry.

The private EgressHub module has no published repository/version. A six-file allowlisted source snapshot in third_party/egresshub,
with per-file SHA256 manifest and relative go.mod replace, makes Docker builds reproducible without an absolute sibling path.
A refresh script copies SDK source/tests only, never operational code, addresses, configuration or keys.
This is a local deployment candidate, not a registry release or permission to publish the private module.
When a stable remote module is explicitly published, migrate to a pinned version dependency.

Production enables Events using existing OAuth and base route, with a distinct certificate identity and renewal timer.
Its key is generated on the consuming node; only CSR/certificate/CA public material cross nodes.
Credentials are directory-mounted read-only, loaded per new connection; CA trust still loads at client construction.

Owner accepted the single-container interruption. Backup includes consistent SQLite and paired master key, original selectors/image,
and an executable rollback which preserves the live database. Readiness failure triggers rollback during cutover.
First startup exposed a root700 mount traversal issue from umask; corrected to755 at the mount root while keeping generation/key private.
SDK probing as container UID is now part of deployment verification. Observed healthy after54.14seconds, not a zero-downtime deployment.

Local full checks, test smoke, actual production UID/network SDK TLS and pre/post existing notification sent200 passed.
Production subscriptions remain zero. Real production Dots execution and forced production certificate renewal were not repeated in this lightweight rollout.
