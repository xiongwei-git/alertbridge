# ADR-014: Publish images only to the existing ACR repository

## Status

Accepted. Supersedes ADR-004 and ADR-007 for new releases; historical images remain untouched.

## Date

2026-10-17

## Context

The owner explicitly chose one ACR release destination rather than GHCR plus an optional mirror. Mainland deployments experienced slow GHCR downloads. Existing repository variables `ACR_IMAGE`, `ACR_USERNAME` and secret `ACR_PASSWORD` already configure the public ACR endpoint. No repository or credential migration is needed.

This Personal Edition repository rejected `application/vnd.oci.empty.v1+json` attestation manifests in release run 39021886202. Alibaba's [edition comparison](https://www.alibabacloud.com/help/en/acr/product-overview/billing-description) distinguishes OCI-artifact hosting from multi-architecture image hosting. We do not assume that generic OCI support means this repository supports BuildKit attestations. An anonymous token exchange followed by a manifest request for the existing v0.5.1 returned HTTP 401 on this date: deployments require authorized registry credentials.

## Decision

- GitHub Actions remains the build/test system; ACR is the only image publication authority. Remove GHCR login, package permissions, mirror job and mirror-only manual trigger. Only a matching stable version tag runs a formal release; pushing main runs ordinary CI, not publication.
- Reuse the existing public `ACR_IMAGE` repository. Reject missing credentials and VPC addresses in CI. Publish full `vX.Y.Z`, `vX.Y`, `latest`, and `vX` only for non-zero major versions. Production pins full versions.
- After all existing tests, Buildx exports one AMD64/ARM64 OCI build with `provenance: mode=max` and `sbom: true`. Validate blob hashes/sizes, platform configs, both evidence types and their subject-to-runtime-digest bindings before any push.
- A small standard-library Python release helper separates the runtime index without changing runtime manifests/layers. Skopeo uploads that local build directly to ACR with `--all --preserve-digests`; there is no intermediate registry and no second build. Every published tag must match the validated runtime index digest exactly. Missing evidence, failed uploads or mismatches fail the release.
- Retain both platforms' SPDX SBOM and BuildKit provenance, original/filtered indexes and runtime digest as a GitHub Actions artifact for 90 days. GitHub artifact attestations bind those files and the ACR runtime index digest to the release workflow; `push-to-registry: false`. Verify with `gh attestation verify <downloaded-file> --repo xiongwei-git/alertbridge` or `gh attestation verify oci://<ACR-image>@sha256:<digest> --repo xiongwei-git/alertbridge` after registry login. BuildKit statements are evidence, not independent signatures; the GitHub attestations provide the signed workflow binding.
- Third-party Actions remain SHA-pinned. Only publication receives `attestations: write` and `id-token: write`; `contents: read` otherwise, no `packages` permission. Skopeo comes from the runner's signed Ubuntu package repository, not a floating third-party Action. No new application dependency.
- Default Compose uses the public ACR address; same-region ECS may explicitly override it with the existing VPC address. Authenticate separately to the endpoint used for pulls. Never store registry credentials in service environment or source.

## Alternatives considered

- Keep the GHCR mirror: rejected by the owner's explicit single-registry decision.
- Disable SBOM/provenance: rejected; registry incompatibility does not justify dropping build evidence or verification.
- Rebuild separately for evidence: rejected because evidence should describe the exact runtime manifests uploaded.
- Test by publishing a new version now: rejected; this change is configuration-only delivery, with no new version/tag/image or server modification authorized.

## Consequences

ACR credential/network failures now block releases rather than being optional. The Personal Edition repository has no SLA, and pulls are not anonymous; public consumers need explicit access. Evidence downloads expire after 90 days, so maintainers/operators should archive them for longer retention. Historical GHCR images and released Compose files remain unchanged; old Compose files need an explicit `ALERTBRIDGE_IMAGE` override. Future tagged releases use the new workflow. Initial ACR publication and GitHub OIDC attestations still require the next authorized formal release to verify live; local registry tests do not prove remote write permission.
