# ADR-013: Optional administrator OIDC login

## Status
Accepted

## Date
2026-10-17

## Context
The administrator needs centralized personal authentication without changing notification ingestion, MCP access tokens or downstream OAuth consent. The runtime must remain a single Go process with one SQLite connection. Identity provider authentication alone must not confer administrator access.

## Decision
Use coreos/go-oidc and golang.org/x/oauth2 for backend Authorization Code Flow with RS256 verification, exact issuer/audience checks, nonce and PKCE S256. Explicit issuer plus subject in a restricted server configuration is the only OIDC administrator binding. Usernames and email claims never grant permissions. Tinyauth subjects are client-specific; verify the deployed provider revision before registering a subject and confirm it through real browser login.

The login page awaits an explicit click. A server switch enables a subdued password fallback during migration. Logout retains the existing CSRF-protected local session deletion and makes no provider-wide logout claim.

Store at most 256 pending login attempts for five minutes in locked process memory; cookies contain only an independent random browser binding. Consume attempts atomically before token exchange. Restart invalidates pending attempts safely. Tokens are discarded after ID Token verification, never persisted in cookies or SQLite. Successful login creates a fresh random session and CSRF token; when an existing session is presented, invalidate it. Normal admin cookies keep their /admin scope and Strict policy; the callback transaction cookie is Secure, HttpOnly, Lax and /auth/oidc scoped.

Use a fixed HTTPS callback and same-issuer HTTPS endpoints; callback host must match configuration. OIDC does not trust forwarded headers or caller-provided external return URLs. Only the existing exact local MCP consent return path is accepted. The notification/MCP/API authentication contracts remain unchanged. No domain-wide auth_request is added.

Use an exact Nginx callback location with access logging disabled and error_log /dev/null so temporary authorization codes cannot enter request logs. Application failures log only fixed categories. Configure the upstream provider to avoid request-query logging too.

## Alternatives considered
A domain-wide auth_request breaks machine callers and obscures application session behavior. A new identity database or Redis adds unnecessary runtime components. Automatic first-user enrollment or username/email grants create an administrator takeover path.

## Consequences
OIDC startup requires working discovery; an invalid configuration fails closed. In-flight logins restart after process restarts. The operator must preserve fallback access and the old image/configuration during rollout. The optional Compose overlay carries a file-backed Secret readable only by UID 10001; production credentials stay outside the repository. This feature does not change business data or existing client credentials.
