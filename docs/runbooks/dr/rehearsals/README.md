# DR rehearsal logs

One file per executed drill, named `YYYY-MM-DD-<type>.md` where
`<type>` is one of `tabletop`, `pitr-drill`, `region-failover`.

## Cadence

| Type | Frequency | Duration | Staging touch? |
|---|---|---|---|
| Tabletop | monthly | 1 h | no |
| Postgres PITR drill | quarterly | ≤4 h | yes, staging |
| Full region failover | biannual | ≤1 day | yes, staging |

## Contents per report

1. Date, type, incident commander, participants, facilitator.
2. Scenario ran (copy from the template).
3. What actually happened — timeline with timestamps.
4. Measurements:
   - Time from "start" to "IC declared stable".
   - RTO achieved vs target.
   - RPO observed (lag / data loss if any).
5. Gaps found — runbook, tooling, comms. Each with an owner +
   follow-up issue/PR link.
6. **Sign-off**: IC name + peer reviewer name + date.

## DoD

A report is valid for merge when:

- All sections above are filled (no TBDs).
- Every gap has a tracked follow-up (issue link).
- IC + peer reviewer signed.
- If RTO/RPO missed targets, at least one remediation PR is linked.
