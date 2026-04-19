# DR Rehearsal — YYYY-MM-DD

**Scenario:** (Postgres failover / region failover / object-store restore / KMS rotation)
**Environment:** staging
**Duration:** HH:MM – HH:MM (UTC)
**Participants:** (incident commander, ops, observer)
**Verdict:** PASS / CONDITIONAL / FAIL

## Target

| Metric | Target (from runbook §10) | Measured | Delta |
|---|---|---|---|
| RTO | e.g. 1h | e.g. 42m | -18m |
| RPO | e.g. 5m | e.g. 3m | -2m |

## Timeline

| Time (UTC) | Event |
|---|---|
| HH:MM | Scenario started — primary killed |
| HH:MM | Promotion command issued |
| HH:MM | First successful read against new primary |
| HH:MM | DNS flipped |
| HH:MM | Full traffic on secondary, degraded path verified |
| HH:MM | End of drill, traffic restored to primary |

## What worked

- (step or procedure that went as documented)

## What didn't

| Issue | Runbook step | Fix |
|---|---|---|
| e.g. `aws rds promote-read-replica` returned AccessDenied | Postgres §3 | Grant `rds:PromoteReadReplica` to sre-oncall role (PR #NNN) |

## Runbook edits filed

- (PR links — one per deviation from the runbook)

## Follow-ups

- (items that aren't runbook edits — capacity planning, tooling gaps, etc.)

## Next rehearsal

- **Scenario:** (next in rotation)
- **Date:** YYYY-MM-DD
- **Owner:** (name)
