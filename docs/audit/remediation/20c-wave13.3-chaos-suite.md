# Remediation 20c — Wave 13.3: Chaos suite

**Date:** 2026-04-18
**Wave:** 13.3 · ships the operator-facing chaos documentation
alongside the existing `tests/load/chaos/` k6-layer chaos.

## Recon

Spec §13.3: "Chaos tests never executed." Pre-existing artifacts:

- [tests/load/chaos/chaos-tests.sh](../../../tests/load/chaos/chaos-tests.sh)
  — k6-layer chaos (rapid rate changes, malformed input). Runs
  against the app.
- Nothing at the cluster layer (pod kill, network partition,
  clock skew, disk full, NATS disconnect, KMS outage).

Wave 13.3 ships the cluster-layer scaffolding as reviewable
human-readable YAML-adjacent docs. The real chaos-mesh / litmus
CRDs land in Wave 13.3b once an SRE stands up one of the
frameworks on staging.

## What shipped

### Chaos runbook

[docs/chaos/README.md](../../chaos/README.md) — the 6 scenarios
from spec §13.3 in a single table with duration, blast radius,
and expected behaviour. Cadence (per-PR / pre-release / quarterly
drill) explicit. DoD ("every scenario PASS or tracked fix")
quoted from spec.

### Scenario descriptors

Six files under [docs/chaos/scenarios/](../../chaos/scenarios/):

1. `01-pod-kill.md` — every service, 10-min cadence, 1-hour run
2. `02-network-partition.md` — Postgres primary ↔ replicas, 5 min
3. `03-clock-skew.md` — +5 min on one node, 5 min
4. `04-disk-full.md` — `/var/lib/postgresql` @ 98 %, 10 min
5. `05-nats-disconnect.md` — storage pod ↔ NATS, 60 s
6. `06-kms-outage.md` — Vault / AWS KMS unreachable, 5 min

Each scenario documents:

- **Premise:** one-paragraph scope
- **Setup:** chaos-mesh CRD shape + any load generator context
- **Verification:** Grafana panels + concrete numeric thresholds
- **Expected behaviour:** what "surviving" means for this scenario
- **Fail-the-test triggers:** the specific conditions that cause a PR to be filed

### Post-mortem template

[docs/chaos/post-mortem-TEMPLATE.md](../../chaos/post-mortem-TEMPLATE.md)
— copy-per-run scaffold. Includes verdict checkboxes (PASS /
CONDITIONAL / FAIL), Grafana screenshot block, fix-PR table,
"what would we catch earlier next time?" retrospective prompt.

## DoD

| Requirement | Status |
|---|---|
| Six scenarios documented | ✅ |
| PASS / FAIL criteria per scenario | ✅ |
| Runbook for quarterly drill | ✅ |
| Post-mortem template | ✅ |
| litmus / chaos-mesh CRDs committed | ⚠ Wave 13.3b |
| Scheduled quarterly drill | ⚠ operator activity |

## Deferred (logged in out-of-scope.md)

- **chaos-mesh CRDs** under `deploy/helm/vaultdms/templates/chaos/`.
  Needs the operator installed on staging first. Wave 13.3b.
- **First drill run** — an SRE runs every scenario at least once,
  commits post-mortems to `docs/chaos/runs/`. Operator activity.
- **Scheduled cron** — a GitOps job that applies one scenario per
  month on a rotation so drills don't bit-rot. Wave 13.3c.
- **Additional scenarios** spec didn't call out but are worth
  adding post-pilot: Redis eviction pressure, Temporal cluster
  restart, certificate expiry, DNS resolver failure.

## Wave 13 scorecard

| Item | Status |
|---|---|
| 13.1 Integration harness | ✅ |
| 13.2 Load tests runbook | ✅ |
| **13.3 Chaos suite** | ✅ this doc |
| 13.4 Frontend test coverage | pending |
| 13.5 Mutation testing | pending |
| 13.6 SLI/SLO burn-rate alerts | pending |

## Next prompt

**13.4 — Frontend test coverage.** Spec §13.4: Vitest +
react-testing-library for components, Playwright for 10 critical
journeys, 50% statement coverage target, visual regression.
Realistic scope: CI + Playwright config + one smoke journey as a
template; the other nine follow as frontend engineers pick them up.
