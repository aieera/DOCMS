# Error Budget Policy

**Owner:** Platform / SRE
**Scope:** All user-facing SLOs tracked in `deploy/monitoring/alerts/slo-burn-rate.yml`.
**Review cadence:** Quarterly, or any time an SLO is changed.

## What this policy does

Defines what happens when an SLO burns through its error budget. The goal
is not to punish — it is to make the reliability/velocity trade-off
explicit and automatic, so no one team has to argue for safety every
time there's pressure to ship.

## SLOs covered

| SLO | Target (30d rolling) | Signal |
|---|---|---|
| api-availability | 99.9% non-5xx across `/api/v1/*` | `http_requests_total{status=~"5.."}` |
| upload-init-latency | 99.9% < 500ms | `http_request_duration_seconds_bucket` |
| search-latency | 99.0% < 400ms (p99) | `http_request_duration_seconds_bucket` |
| permission-latency | 99.9% < 50ms (p99) | `permission_check_duration_seconds_bucket` |
| ocr-latency | 95.0% < 30s (p95) | `ocr_job_duration_seconds_bucket` |

Each SLO's error budget = `(1 - target) * total_events_in_30d`.
For api-availability at 99.9%, that's ~43m of downtime per 30d.

## Budget states

The burn-rate alerts output an implicit budget consumption. Policy
actions attach to the **remaining budget** computed over the trailing
30-day window:

| State | Remaining budget | Action |
|---|---|---|
| Healthy | > 50% | Normal velocity. No constraints. |
| Caution | 20–50% | Feature PRs go through as normal, but every PR touching the burning SLO's service needs an explicit reviewer check of "does this change reliability of the SLO path." |
| **Exhausted** | **< 20%** | **All non-critical feature work on the affected service pauses.** Only reliability fixes, rollback of the change(s) that caused the burn, and critical security fixes merge. Product escalates to platform lead. |
| Overdrawn | 0% | As Exhausted, plus: no deploys to production on the affected service without on-call approval per deploy until the budget recovers above 20%. |

## What "pauses" means

- New feature branches for the affected service stop merging.
- In-flight feature PRs on the affected service stop being reviewed until
  the budget recovers — they aren't closed, just parked.
- Reliability work *on the affected service* continues (often
  accelerated).
- Other services are unaffected.

This is enforced socially (tech lead + on-call) rather than via CI
blocks. A CI check that inspects budget state and soft-warns on PRs is
on the Wave 13.6b backlog.

## Who decides

- **Budget state transitions** are observable from the SLO dashboard —
  no human decision is needed to move between states.
- **Pause/unpause of feature work** is the on-call engineer's call,
  confirmed by the platform lead in the same 24h.
- **Exception merges during Exhausted state** require written approval
  (PR comment) from both the service tech lead and the platform lead.
  These exceptions are tracked in the weekly reliability review.

## How we recover budget

The budget is rolling 30d — it refills as the 30d window slides forward
past the incident. An incident on day 1 stops affecting the budget
calculation after day 31. There is no way to "pay back" budget; we just
wait the window out while keeping the service stable.

## When to revise a target

If an SLO is chronically in Caution or Exhausted despite good-faith
reliability work, the target is wrong for the actual product need. That
is a legitimate reason to lower the target — **once**, with explicit
stakeholder signoff (product + platform + whoever the contractual SLA
lives with, if any). Document the change in this file with a date and a
link to the discussion.

Silently lowering targets to avoid pauses is the failure mode this
policy is designed to prevent.

## Pointers

- Alert rules: `deploy/monitoring/alerts/slo-burn-rate.yml`
- Burn runbook: `docs/slo/pause-runbook.md`
- Dashboard: Grafana → SLO Overview (Wave 13.6 panel, pending first deploy)
