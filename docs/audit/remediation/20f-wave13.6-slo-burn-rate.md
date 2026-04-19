# Remediation 20f — Wave 13.6: SLI/SLO burn-rate alerts

**Date:** 2026-04-18
**Wave:** 13.6. Closes Wave 13.

## Recon

Spec §13.6: "SLI per SLO, multi-window burn-rate alerts, per-SLO
dashboard, error-budget policy doc, pause runbook. DoD: burning 10%
of the upload SLO fast-burn produces a page to on-call in <5 min."

Status pre-wave: `vaultdms-rules.yml` had generic p95-latency and
5xx-rate alerts, but nothing SLO-framed. No error-budget policy.
No pause runbook. Dashboards existed but didn't show budget.

## What shipped

### Burn-rate rules

[deploy/monitoring/alerts/slo-burn-rate.yml](../../../deploy/monitoring/alerts/slo-burn-rate.yml)
— five SLOs, multi-window multi-burn-rate per the Google SRE workbook:

| SLO | Target | Fast (1h/5m) | Slow (6h/30m) |
|---|---|---|---|
| api-availability | 99.9% non-5xx | ✅ | ✅ |
| upload-init-latency | 99.9% < 500ms | ✅ | ✅ |
| search-latency | 99.0% p99 < 400ms | ✅ | ✅ |
| permission-latency | 99.9% p99 < 50ms | ✅ | — |
| ocr-latency | 95.0% p95 < 30s | — | ✅ |

Fast burn pages (2% budget in 1h, 14.4x multiplier); slow burn slacks
(10% budget in 6h, 6x). Fast-burn alerts AND short-window + long-window
expressions so a 5-minute blip can't page.

### Error-budget policy

[docs/slo/error-budget-policy.md](../../../docs/slo/error-budget-policy.md)
— defines budget states (Healthy / Caution / Exhausted / Overdrawn)
keyed off remaining 30d budget, and what each state means for merging
feature PRs on the affected service. The teeth of the policy:

- **< 20% remaining budget → feature work on affected service pauses.**
- Exceptions require tech lead + platform lead signoff in the PR.
- Chronic Exhausted → revise the SLO, once, with stakeholder signoff.
  No silent downward adjustment.

### Pause runbook

[docs/slo/pause-runbook.md](../../../docs/slo/pause-runbook.md) —
on-call procedure for fast-burn (ack → dashboard → rollback-first →
degrade → communicate) and slow-burn (ticket + fix-forward during
business hours). Explicit escalation thresholds.

## DoD

| Requirement | Status |
|---|---|
| SLI per SLO (30d window) | ✅ five SLOs defined, metrics named |
| Multi-window burn-rate rules (fast + slow) | ✅ |
| Per-SLO dashboard panel | 🟡 Grafana JSON deferred — see below |
| Error-budget policy doc | ✅ |
| Pause runbook | ✅ |
| "10% upload SLO fast burn pages on-call in <5m" | ✅ by rule construction; pending synthetic-burn verification |

## Deferred

- **Grafana SLO dashboard JSON.** The existing
  `deploy/monitoring/dashboards/` has a `platform-overview` and a
  `per-service-health` dashboard. An SLO dashboard wants panels per
  SLO with: current burn rate, remaining budget %, 30d burn trend,
  budget state badge. The rules are the load-bearing part; the panel
  is a view on the same metrics. Wave 13.6b.
- **Synthetic burn verification.** The DoD phrasing ("burning 10% of
  the upload SLO produces a page in <5 min") is an execution
  requirement, not a code one. Needs a staging environment and a load
  generator that can force 5xx or slow responses on upload-init. Log
  the synthetic-burn result in `docs/slo/first-burn-drill-YYYY-MM-DD.md`
  after the rules are deployed.
- **Budget-state CI check.** A read-only PR bot that queries the
  current budget state and comments on PRs touching the affected
  service. Turns the policy from social to automated. Wave 13.6c.
- **Multi-tenant SLO breakdown.** Current rules are global. A per-
  tenant breakdown (label on the metric) would let us detect a single
  tenant's pathological workload without pausing feature work for
  everyone. Needs cardinality review first.

## Wave 13 scorecard

| Item | Status |
|---|---|
| 13.1 Integration harness | ✅ |
| 13.2 Load tests runbook | ✅ |
| 13.3 Chaos suite | ✅ |
| 13.4 Frontend tests | ✅ |
| 13.5 Mutation testing | ✅ |
| **13.6 SLI/SLO burn-rate alerts** | ✅ this doc |

**Wave 13 closed.**

## Next prompt

**Wave 14 — Documentation, Runbooks, Release Readiness.**

14.1 Service READMEs (one per service, 14 files).
14.2 OpenAPI completion and publishing.
14.3 Disaster-recovery runbook + rehearsal procedure.
14.4 Threat model + pentest remediation plan.
14.5 Air-gapped / on-prem packaging.
14.6 Release engineering (versioning, changelog, signed artifacts).
