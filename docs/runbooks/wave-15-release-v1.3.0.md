# Runbook — Cutting `v1.3.0` (Wave 15)

Operator-ops runbook. This is the checklist that closes the Wave 15
Final Acceptance Gate once the engineering work in
`WAVE_15_PROGRESS.md` is complete.

## Prerequisites

- [ ] `WAVE_15_PROGRESS.md` shows no **not-started** rows. Everything
      remaining must be either **done** or explicitly **deferred to
      post-G2** with an out-of-scope.md entry.
- [ ] `make test` green on `main`.
- [ ] `make test-integration-wave15` green on a docker-capable runner.
- [ ] `make lint` clean.
- [ ] `buf lint && buf breaking` clean, proto stubs regenerated,
      `scripts/openapi/check-drift.sh` clean.
- [ ] `scripts/mutesting/run.sh` passes threshold (70%). If the new
      packages score below threshold, either add tests that kill the
      surviving mutants or lower the threshold with reviewer sign-off
      before cutting the release.
- [ ] `docs/demo/wave-15.mp4` (or Loom link embedded in
      `docs/demo/wave-15-script.md`) exists.

## Step 1 — First `release-please` run

Wave 15 is the first release cohort that flows through the
release-please workflow committed 2026-04-20. Expect ~18 release PRs
(one per package including the new `services/acknowledgement`).

1. Confirm the GitHub Action `.github/workflows/release-please.yml`
   is enabled.
2. Trigger a manual run (`gh workflow run release-please.yml`) or
   wait for the next push to `main`.
3. release-please opens one PR per package. Review each — most will
   be `chore(release): auth 0.2.0` style bumps that auto-generate
   CHANGELOG.md entries from Conventional Commits.
4. Merge PRs in this order to keep CHANGELOG dates monotonic:
   `services/acknowledgement` → `services/auth` → `services/policy`
   → `services/signature` → `services/workflow` → rest.
5. After each merge, release-please pushes a tag like
   `acknowledgement-v0.1.0` which triggers `.github/workflows/release.yml`
   to build the container image + emit SLSA provenance.
6. Wait for all image builds to report green.

## Step 2 — Umbrella `v1.3.0` tag

Once per-service tags exist, cut the umbrella version:

```sh
git tag -s v1.3.0 -m "Wave 15 — enterprise additions (acknowledgements, geofencing, force-password-change, saved signatures)"
git push origin v1.3.0
```

Release workflow picks up the tag, builds a multi-arch umbrella
image, and attaches the `slsaprovenance.v1` attestation.

## Step 3 — cosign sign the artefacts

For each service image built during the cohort + the umbrella image:

```sh
export COSIGN_EXPERIMENTAL=1
for img in $(cat release-images.txt); do
  cosign sign --yes "$img"
  cosign attest --yes --type slsaprovenance --predicate slsa.json "$img"
done
```

The keyless OIDC flow uses the GitHub Actions identity that produced
the image; the signatures land in the Rekor transparency log.

Verify a signature before publishing:

```sh
cosign verify \
  --certificate-identity-regexp='https://github.com/aieera/DOCMS/.github/workflows/release.yml@refs/tags/v1.3.0' \
  --certificate-oidc-issuer='https://token.actions.githubusercontent.com' \
  ghcr.io/aieera/docms/vaultdms-umbrella:v1.3.0
```

## Step 4 — GitHub release body

release-please auto-generates one, but replace the body with the
curated Wave 15 changelog:

```
## Wave 15 — Enterprise additions

- **Force-change-password** (15.3) — first-login / admin-reset / expiry
  rotation with `password_history` reuse check, per-tenant
  configurable expiry. Temporal sweeper at 02:00 UTC per tenant.
- **Geofencing** (15.2) — tenant / workspace / document scoped
  country + CIDR policies with allow / deny / step-up modes. 451 +
  428 enforcement at the API gateway and WebSocket handshake.
- **Acknowledgement campaigns** (15.1) — tamper-evident attestations
  with per-assignment HMAC under a KMS-wrapped tenant signing key +
  per-campaign SHA-256 hash chain. Reminder + escalation Temporal
  schedule at 09:00 UTC per tenant.
- **Saved signatures** (15.4) — drawn / uploaded / typed signature
  profiles, per-profile DEK wrapped under the tenant KEK, crypto-
  shred on delete. Wave 9 PAdES cryptography unchanged.

### Security

- New threat-model section B9 covers all four additions with STRIDE
  analysis (`docs/security/threat-model.md`).
- Cross-tenant RLS regression tests in
  `make test-integration-wave15`.

### Observability

- Dashboard `deploy/monitoring/dashboards/wave-15-features.json`.
- Alerts `ops/prometheus/rules/wave-15.yml`.
```

## Step 5 — Deferred / post-release follow-ups

Record in `docs/backlog/out-of-scope.md` under a `v1.3.x` header:

- MaxMind GeoLite2 adapter + GeoLite refresh schedule.
- Wave 9 envelope UI + saved-signature picker drop-in.
- Notification service wiring for email + inbox ≤ 60 s
  acknowledgement distribution.
- Adobe Reader PAdES-B-LT validation harness in CI.
- Chaos drill execution with scenario 08 (ack reminder worker kill).

## Rollback

If a post-release bug fires:

```sh
git revert <release-commit>
git tag -s v1.3.1 -m "Revert — <bug summary>"
git push origin v1.3.1
```

Disable the affected feature flag in `organizations` config
(`password_expiry_days=0`, delete the geofence policy row, close the
campaign, revoke the signature profile) rather than rolling back
the binary when possible — the four Wave 15 additions are additive
and can be turned off per-tenant without a redeploy.
