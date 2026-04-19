# Remediation 21c — Wave 14.3: Disaster-recovery runbook

**Date:** 2026-04-18
**Wave:** 14.3.

## Recon

Spec §14.3: "RTO/RPO targets per data store, documented restore
procedures, quarterly rehearsal schedule. DoD: the first rehearsal
meets the documented RTO within 2×."

Status pre-wave: no consolidated DR runbook. Service-specific runbooks
covered ops (NATS topology, key management, retention). Cross-region
replication exists in infra (Postgres read replicas, S3 CRR, NATS
mirror streams) but with no one-page recovery procedure.

## What shipped

### Runbook

[docs/runbooks/10-disaster-recovery.md](../../runbooks/10-disaster-recovery.md)
— single authoritative recovery document. Covers:

- **RTO/RPO table** for seven data stores (Postgres, object store,
  OpenSearch, NATS, Redis, KMS, Temporal). Targets justified by
  business impact + replication mechanism.
- **Four concrete procedures** with commands: total region loss,
  Postgres-only failover, object-store corruption, KMS compromise.
  Commands are `aws`, `kubectl`, `nats`, `psql` — runnable, not
  pseudocode.
- **Known gaps** — residency (EU tenants can't fail to US), partial-
  failover split brain, no tertiary region. Flagged rather than
  hidden.

### Rehearsal template

[docs/runbooks/dr-rehearsal-TEMPLATE.md](../../runbooks/dr-rehearsal-TEMPLATE.md)
— fill-in scaffold: target vs measured RTO/RPO, timeline, "what
didn't work" table with runbook-edit PR links, follow-ups, next
rehearsal assignment.

### Rehearsal schedule

Committed in the runbook:

| Quarter | Scenario |
|---|---|
| Q2 2026 (target 2026-05-15) | Postgres failover in staging |
| Q3 2026 | Full region failover in staging |
| Q4 2026 | Object-store restore |

Each drill produces a `dr-rehearsal-YYYY-MM-DD.md`. Rehearsal fail
criteria are explicit (RTO >2× target, improvised commands, data
loss >RPO, step fails due to drift).

## DoD

| Requirement | Status |
|---|---|
| RTO/RPO per data store | ✅ |
| Documented restore procedures | ✅ |
| Quarterly rehearsal schedule | ✅ |
| First rehearsal passes RTO within 2× | 🟡 pending first drill (Q2 2026) |

DoD item 4 is operator activity — not a pre-merge artifact. The first
drill log, due 2026-05-15, will close it.

## Deferred

- **Status-page templates** — `docs/runbooks/templates/` for the
  customer-facing DR announcement (acknowledge, update, resolve).
  Wave 14.3b.
- **Automated DR drill harness** — a scripted Postgres-kill scenario
  that can be run on demand in staging. Low priority until we've
  done at least one manual drill and know what to automate.
- **Residency-aware DR** — EU tenant failover needs an EU secondary
  region (Wave 14.3c). Not solvable with docs alone; needs infra +
  compliance sign-off.
- **Tertiary region** — loss of both primary and secondary is out of
  scope. Wave 15+.

## Wave 14 scorecard

| Item | Status |
|---|---|
| 14.1 Service READMEs | ✅ |
| 14.2 OpenAPI completion | 🟡 rail shipped |
| **14.3 DR runbook + rehearsal** | ✅ this doc (first drill pending) |
| 14.4 Threat model + pentest plan | pending |
| 14.5 Air-gapped / on-prem packaging | pending |
| 14.6 Release engineering | pending |

## Next prompt

**14.4 — Threat model + pentest remediation plan.** Spec §14.4:
STRIDE per trust boundary, known-issues register, pentest
engagement scope, remediation SLA per severity.
