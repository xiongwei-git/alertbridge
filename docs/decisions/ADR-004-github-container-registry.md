# ADR-004: Publish official images through GitHub Container Registry

## Status

Accepted

## Date

2026-07-15

## Context

Building AlertBridge on every production server increases deployment time, downloads the Go toolchain and module graph, and makes a small VPS responsible for producing its own release artifact. Users need a versioned image that can be pulled directly by Docker Compose on both common VPS architectures.

The source repository already lives on GitHub. Maintaining an additional Docker Hub account, access token, repository and release path would duplicate credentials and operational work without improving the initial deployment target. Docker Hub's native automated-build feature is also deprecated in favor of CI workflows.

## Decision

- GitHub Actions is the only official CI and image-build system.
- GitHub Container Registry at `ghcr.io/xiongwei-git/alertbridge` is the only official image registry.
- A semantic version tag such as `v0.2.0` triggers tests, static analysis, Docker E2E, and then a multi-architecture publish for `linux/amd64` and `linux/arm64`.
- Published tags include the full version, major/minor, non-zero major and `latest`. The floating major tag is suppressed throughout `v0.x`, while production installations always pin the full version through `ALERTBRIDGE_IMAGE_TAG`.
- GitHub's repository-scoped `GITHUB_TOKEN` publishes the image. Only the publishing job receives `packages: write`, `attestations: write` and `id-token: write`; other jobs remain read-only.
- Third-party Actions are pinned to immutable commit SHAs. Published images include OCI metadata, SBOM, BuildKit provenance and a GitHub artifact attestation.
- `compose.yaml` defaults to the official image and accepts the non-authoritative ACR mirror override defined by ADR-007. Local source builds remain available through the explicit `compose.build.yaml` overlay.

## Alternatives Considered

### Build from source on every server

This remains a fallback for contributors but is not the production default because it is slower, consumes more bandwidth and couples deployment to build infrastructure.

### Publish only to Docker Hub

Rejected because it requires separate credentials and repository administration while the source and CI already live on GitHub.

### Publish to both GHCR and Docker Hub

Rejected for the initial release because two registries double publishing permissions, failure modes and documentation. A mirror can be reconsidered only if real user demand justifies it.

## Consequences

- A normal deployment pulls a small prebuilt image instead of compiling Go code.
- The first GHCR package must be made public once; public images can then be pulled without authentication.
- Release tags are immutable operational inputs. A broken tag must be replaced by a new patch version, never by rewriting an existing version.
- `latest` is convenient for evaluation, but production upgrade and rollback instructions use explicit versions.
- Maintainers must update `VERSION` before creating a matching Git tag.
