# Changelog

All notable changes to VaultDMS are listed here.

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning: [SemVer 2.0](https://semver.org/spec/v2.0.0.html).
Policy: [docs/release/release-policy.md](docs/release/release-policy.md).

## [Unreleased]

### Added
- Wave 14.6: release workflow now signs every image via Sigstore
  cosign (keyless OIDC), generates SPDX SBOMs via Syft, attests the
  SBOM to each image, and attaches the SBOMs + air-gap bundle to the
  GitHub Release.
- Wave 14.5: `scripts/airgap/build-bundle.sh` builds an offline-
  installable tarball (services + third-party images + chart +
  load.sh). Documented in `docs/deploy/airgap-install.md`.
- Wave 14.4: STRIDE threat model across 8 trust boundaries,
  known-issues register (KI-01…KI-05), pentest scope + remediation
  SLA.
- Wave 14.3: consolidated disaster-recovery runbook with RTO/RPO
  targets, recovery procedures, quarterly rehearsal schedule.
- Wave 14.2: OpenAPI drift detector (`scripts/openapi/check-drift.sh`),
  spectral lint in CI. Per-service spec backfill in progress.
- Wave 13.6: multi-window SLO burn-rate alert rules for five SLOs;
  error-budget policy; pause runbook.
- Wave 13.5: go-mutesting mutation-test runner + CI job against
  auth/policy/storage packages with 70% threshold.
- Wave 13.4: Playwright e2e scaffolding with first journey, Vitest
  coverage gate (50% statements).
- Wave 13.3: chaos scenario suite (pod-kill, network-partition,
  clock-skew, disk-full, nats-disconnect, kms-outage) with
  post-mortem template.
- Wave 13.2: k6 load-test scenarios + runbook + baseline template.
- Wave 13.1: shared integration-test harness in `pkg/testharness/`
  with a dedicated integration-tests CI job.

### Changed
- Release workflow image matrix now covers all 16 services (added
  audit, collaboration, intelligence, preview, signature-signer,
  web).

### Deprecated
- (none)

### Removed
- (none)

### Security
- (none in this cycle; pentest engagement pre-GA pending)

## Earlier waves

Per-wave details live in `docs/audit/remediation/*.md`. This changelog
starts at Wave 13 because the project was pre-release through Wave 12.
First semver-tagged release is targeted v1.0.0-rc.1 after Wave 14.6.
