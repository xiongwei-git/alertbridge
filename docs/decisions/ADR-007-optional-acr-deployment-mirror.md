# ADR-007: Keep ACR as an optional domestic deployment mirror

## Status

Accepted

## Date

2026-07-16

## Context

AlertBridge publishes small multi-architecture images to GHCR. A production ECS instance in mainland China can reach GitHub's API but may intermittently stall while downloading image layers from GHCR's CDN. Adding unrelated Docker Hub mirror endpoints does not reliably accelerate GHCR and introduces an unnecessary third-party trust boundary.

The official image, provenance and release lifecycle must remain on GitHub. The domestic server nevertheless needs a predictable same-region pull path without compiling source or changing the container security model.

## Decision

- GHCR remains the only official image registry and source of release provenance.
- A release may copy the already-published GHCR multi-architecture image to an authenticated ACR repository configured through the `ACR_IMAGE` and `ACR_USERNAME` repository variables and the `ACR_PASSWORD` repository secret.
- The ACR job consumes the immutable GHCR digest produced by the official publishing job. It does not rebuild source.
- Failure or absence of the optional ACR configuration does not invalidate the official GHCR release.
- `compose.yaml` defaults to GHCR and accepts an optional `ALERTBRIDGE_IMAGE` override. Mainland production can use an ACR VPC image path while retaining the same explicit `ALERTBRIDGE_IMAGE_TAG` upgrade and rollback workflow.
- ACR credentials stay outside the repository and service environment. GitHub Actions receives the push credential as a repository secret; each Docker host authenticates separately for pulls.

## Alternatives Considered

### Add more public Docker registry mirrors

Rejected because common mirror configuration targets Docker Hub rather than GHCR, availability is outside project control, and an untrusted mirror expands the image supply-chain boundary.

### Make ACR a second official registry

Rejected because ACR Personal Edition has no production SLA and uses long-lived access credentials. Maintaining two authoritative registries would also make provenance and release-status decisions ambiguous.

### Build the image again in ACR

Rejected because separate builds can produce different indexes and duplicate the test and build path. Copying the published digest preserves GHCR as the release authority.

### Transfer every release manually over SSH

Retained as an emergency fallback, but not the routine path because it depends on a maintainer workstation and cannot support unattended upgrades.

## Consequences

- Mainland ECS deployments can pull through a same-region ACR VPC endpoint after a one-time Docker login.
- ACR is a convenience cache, not an independent release authority; version selection and integrity investigation start from GHCR.
- Operators must configure and rotate the ACR fixed password in GitHub and on each Docker host.
- Production still pins a full version tag. ACR failures are visible in the release workflow without blocking the official release.
