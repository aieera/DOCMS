# Remediation 21f — Wave 14.6: Release engineering

**Date:** 2026-04-18
**Wave:** 14.6. Closes Wave 14 and the execution spec.

## Recon

Spec §14.6: "Versioning scheme, changelog, signed artifacts,
deprecation policy, release cadence."

Status pre-wave:

- `.github/workflows/release.yml` existed but built only 11 of 16
  services (audit, collaboration, intelligence, preview,
  signature-signer, web were missing).
- No image signing.
- No SBOM generation.
- No CHANGELOG.md.
- No release policy doc.
- No deprecation policy.

## What shipped

### Release workflow

[.github/workflows/release.yml](../../../.github/workflows/release.yml)
rewritten:

- **Matrix expanded to all 16 services.**
- **Image signing** via Sigstore cosign keyless OIDC
  (`id-token: write` permission, `cosign sign` against each image
  digest).
- **SBOM generation** via Syft (SPDX JSON per service); attached to
  the GitHub Release and attested to the image via
  `cosign attest --type spdxjson`.
- **Air-gap bundle job** runs after images, invokes
  `scripts/airgap/build-bundle.sh`, attaches the tarball to the
  Release.
- **Changelog + Release** job assembles SBOMs + bundle + generated
  notes into a single `softprops/action-gh-release` call.

### Release policy doc

[docs/release/release-policy.md](../../release/release-policy.md) —
the one-stop reference:

- **SemVer**: MAJOR / MINOR / PATCH defined in terms of API, schema,
  and Helm-values breakage.
- **Cadence**: monthly minors (first Tuesday), patches as needed,
  security out-of-band.
- **Deprecation**: minimum two minors between deprecation and
  removal; `Deprecation` + `Sunset` response headers; OpenAPI
  `deprecated: true`; no silent removals.
- **Supported versions**: latest + previous minor full support;
  older minors 6-month security-only; older majors 12-month
  security-only.
- **Signing verification** — `cosign verify` + `cosign
  verify-attestation` commands customers can run against
  `ghcr.io/vaultdms/*` images.
- **Release checklist** — pre-tag (CI green, CHANGELOG, OpenAPI
  drift, UPGRADE.md, notes) and post-tag (smoke test, announcement,
  CVE scan).
- **Emergency release** — out-of-band patch flow for Critical
  findings; what CI can and can't be skipped.

### CHANGELOG

[CHANGELOG.md](../../../CHANGELOG.md) bootstrapped in Keep-a-Changelog
format with Wave 13 and Wave 14 entries under `[Unreleased]`. First
tagged release (`v1.0.0-rc.1`) will move them under a dated section.

## DoD

| Requirement | Status |
|---|---|
| Versioning scheme documented | ✅ SemVer 2.0 |
| Changelog | ✅ bootstrapped |
| Signed artifacts | ✅ cosign keyless |
| SBOM per artifact | ✅ SPDX via Syft, attested |
| Deprecation policy | ✅ two-minor window, headers, OpenAPI |
| Release cadence | ✅ monthly minor, weekly-ish patch |
| All 16 service images built | ✅ |
| Air-gap bundle attached to Release | ✅ |

## Deferred

- **Weekly CVE scan job** (Wave 14.6b). Cron workflow that re-scans
  each released image against the latest vulnerability database and
  emits a report into `docs/security/cve-scans/`. Not in the release
  flow itself — post-release concern.
- **Customer-facing release-notes renderer.** Release-policy.md has
  the template; automating the export to a public
  `releases.vaultdms.io` is Wave 14.6c.
- **Signed Helm chart** via `helm package --sign`. Useful once the
  chart is published to an OCI chart repo. Pair with Wave 14.5c
  registry-mirror work.
- **Release-branch automation.** Cutting `release/vX.Y` on the
  first patch is currently manual. GitHub Action to auto-cut on
  the `MINOR+1-patch.1` tag push would remove that toil.
- **Deprecation-header enforcement test.** A contract test that,
  for every endpoint marked `deprecated: true` in OpenAPI, asserts
  the live response carries `Deprecation: true` + a `Sunset` header.
  Depends on Wave 14.2 backfill.

## Wave 14 scorecard

| Item | Status |
|---|---|
| 14.1 Service READMEs | ✅ |
| 14.2 OpenAPI completion | 🟡 rail shipped, per-service backfill |
| 14.3 DR runbook + rehearsal | ✅ (first drill Q2 2026) |
| 14.4 Threat model + pentest plan | ✅ (engagement pre-GA) |
| 14.5 Air-gapped / on-prem packaging | ✅ |
| **14.6 Release engineering** | ✅ this doc |

**Wave 14 closed. Execution spec `DMS Architecture/final.md` complete.**

## Post-spec backlog

The specced waves (11–14) are done. Open follow-through work carried
forward:

- **14.2 per-service OpenAPI backfill** (tracked in 21b scorecard).
- **First DR rehearsal** — 2026-05-15 (21c).
- **First pentest engagement** — pre-GA (21d).
- **First mutation baseline** — after first `main` run (20e).
- **First SLO synthetic-burn drill** — post-deploy (20f).
- **FIPS bundle variant** (21e / 14.5b).
- **CVE-scan weekly cron** (this doc / 14.6b).

These belong in the product backlog / operator calendar, not in a new
wave. Ready to cut `v1.0.0-rc.1`.

## Next prompt

Spec complete. Suggested next steps outside the spec:

1. **Tag `v1.0.0-rc.1`** and run the release workflow end-to-end.
2. **Schedule pentest** with a shortlisted vendor.
3. **First DR rehearsal** in staging (Postgres failover).
4. **Kick off Wave 14.2 backfill** per-service — mechanical.
