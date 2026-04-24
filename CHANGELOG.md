# Changelog

All notable changes to VaultDMS are listed here.

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning: [SemVer 2.0](https://semver.org/spec/v2.0.0.html).
Policy: [docs/release/release-policy.md](docs/release/release-policy.md).

## [Unreleased]

### Wave 15 highlights (cut as v1.3.0)

Four sub-waves, shipped 2026-04-20 → 2026-04-24. Full per-commit
notes below; this section is the operator-facing summary.

- **15.1 — Policy attestation campaigns** (`services/acknowledgement`).
  New Go service for tenant-admin-driven policy acknowledgement
  campaigns. Per-tenant HMAC + per-campaign SHA-256 hash chain so
  row deletion and timestamp tampering are detectable on export
  (ADR 0027). Daily 09:00 UTC Temporal reminder + 7-day escalation.
  Dual emission: `dms.acknowledgement.*.v1` domain events +
  `dms.notify.acknowledgement.*.v1` fan-out (ADR 0032 carve-out).

- **15.2 — Geofencing** (`pkg/geo` + `services/policy`). CIDR + country
  allow/deny/step-up policies evaluated per request via OPA rule in
  Rego. Middleware returns 451 Unavailable-For-Legal-Reasons on
  deny, 428 Precondition Required on step-up, 503 on decider error
  (fail-closed). MaxMind adapter behind `//go:build maxmind` for
  countries; static CIDR path is the always-on default (ADR 0028).

- **15.3 — Password-lifecycle policy** (`services/auth`). Admin-forced
  rotation issues a single-use change token with 10-minute TTL +
  Redis GETDEL replay protection. Password history (default 12) +
  min-age (default 1 day). Emits `dms.auth.password_reset_requested.v1`
  for SOC monitoring (ADR 0029).

- **15.4 — Saved signature profiles** (`services/signature`). Users
  draw/upload/type a signature once; the cryptographic PAdES
  signature is unchanged. Image bytes encrypted at rest with a
  per-profile DEK KMS-wrapped under the tenant KEK; delete is
  crypto-shred (ADR 0030). Tier-1 structural PAdES validator ships
  under `//go:build pades_corpus`; Tier-2 cryptographic acceptance
  remains the operator-per-release Adobe Reader + EU DSS flow.

#### Platform hardening in the same release window

- **ADR 0031 — internal-auth plane**: `pkg/internalauth` +
  `pkg/trustedproxy` consolidate `/internal/*` authentication (mTLS
  or HMAC, dual-mount during rollout) and client-IP resolution
  (CIDR-aware XFF walk, fail-closed in production). Closes T-D-1
  and T-D-2.
- **T-D-3 … T-D-9 closed**: acknowledgement service split by seam;
  signature-profile orphan sweeper (daily 03:00 UTC); package-local
  HTTP client in workflow activities + `forbidigo` lint for
  `http.DefaultClient`; mandatory `Resolver` config in
  acknowledgement; PAdES prod_accept gate; typed Temporal
  AlreadyExists check; ADR 0032 for subject namespaces + CI lint.

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
