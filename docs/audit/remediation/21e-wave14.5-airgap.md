# Remediation 21e — Wave 14.5: Air-gapped / on-prem packaging

**Date:** 2026-04-18
**Wave:** 14.5.

## Recon

Spec §14.5: "Offline-installable bundle, Helm values template,
offline-mode docs, license gating."

Status pre-wave:

- `deploy/helm/sedoc/values-airgapped.yaml` exists (72 lines) but
  only flips `imagePullPolicy` + disables telemetry — no bundle
  build, no install runbook, no license flow.
- `deploy/helm/sedoc/values-onprem.yaml` exists (50 lines).
- No script to produce a loadable image bundle.
- No customer-facing install docs.

## What shipped

### Bundle build script

[scripts/airgap/build-bundle.sh](../../../scripts/airgap/build-bundle.sh)
— produces `dist/sedoc-airgap-${VERSION}.tar.gz` containing:

- All 16 SeDoc service images (`docker save`'d).
- Seven pinned third-party images (postgres, redis, nats, opensearch,
  minio, temporal auto-setup, temporal admin-tools).
- Full Helm chart.
- `load.sh` — loads images into the customer's Docker daemon,
  re-tags to their private registry, pushes.
- A copy of the install runbook bundled as `README.md`.

Multi-arch support via `PLATFORM` env var (default `linux/amd64`;
`linux/arm64` produces a separate bundle).

### Install runbook

[docs/deploy/airgap-install.md](../../deploy/airgap-install.md) —
customer-facing, covers:

- Pre-reqs (K8s 1.27+, private registry, storage sizing).
- Transfer + SHA256 verify.
- `load.sh` push flow.
- `helm install` with the private-registry override.
- Admin bootstrap via `vaultdms-ctl admin bootstrap`.
- Smoke test checklist.
- **License section** — offline JWT, no phone-home, tamper-detect
  via signature.
- **Updates** — bundle-based, no in-place pull.
- **Hardened / classified deployments** — FIPS mode, customer-signed
  bundles available on request.
- Troubleshooting (ImagePullBackOff, license signature, Postgres
  PVC, migrations).

## DoD

| Requirement | Status |
|---|---|
| Offline-installable bundle | ✅ build script + documented contents |
| Helm values for air-gap + on-prem | ✅ pre-existing, referenced |
| Offline-mode docs | ✅ |
| License gating | ✅ documented; enforcement code pre-exists |
| Multi-arch (amd64 + arm64) | ✅ via PLATFORM env |
| FIPS variant | 🟡 docs mention; FIPS Go toolchain bundle is Wave 14.5b |

## Deferred

- **FIPS bundle build.** Needs a separate CI job using the FIPS-
  validated Go toolchain. Wave 14.5b. Most customers don't need it;
  the ones who do have it as a contractual line item.
- **Bundle signing.** Raabyt-signed or customer-signed bundles —
  `cosign sign` + in-bundle verification manifest. Wave 14.6 release-
  engineering owns signing infra.
- **Upgrade rehearsal.** Like DR rehearsal (Wave 14.3), customer
  upgrades need to be rehearsed quarterly in staging. Calendar item.
- **Registry-mirror workflow.** Some customers run an internal mirror
  that can proxy `ghcr.io` — for them `load.sh` is overkill; they
  just need a manifest of image digests to pull through their mirror.
  Wave 14.5c.
- **Helm Chart repo hosting.** For customers who *can* reach
  `charts.vaultdms.io`, publishing the chart there avoids
  hand-copying. Wave 14.6.

## Wave 14 scorecard

| Item | Status |
|---|---|
| 14.1 Service READMEs | ✅ |
| 14.2 OpenAPI completion | 🟡 rail shipped |
| 14.3 DR runbook + rehearsal | ✅ |
| 14.4 Threat model + pentest plan | ✅ |
| **14.5 Air-gapped / on-prem packaging** | ✅ this doc |
| 14.6 Release engineering | pending |

## Next prompt

**14.6 — Release engineering.** Spec §14.6: versioning scheme,
changelog, signed artifacts, deprecation policy, release cadence.
Last item in Wave 14 and in the whole spec.
