# Chaos 08 — Acknowledgement reminder worker kill mid-sweep

## Premise

Kill the `workflow-worker` pod mid-run during the daily acknowledgement
reminder sweep. The Wave 15.1 contract is: **no duplicate reminded.v1
or escalated.v1 emissions** across the retry + restart — even when
Temporal redelivers the whole workflow.

Idempotency comes from the sweep SQL in
[services/acknowledgement/internal/service/service.go](../../../services/acknowledgement/internal/service/service.go)
(`Service.SweepReminders`):

- Reminders use `reminded_at IS NULL OR reminded_at::date < $2::date`
  as the guard, so a second run on the same day won't re-increment
  `reminded_count` or re-emit.
- Escalations use `escalated_at IS NULL` as a one-shot, so a second
  run on an already-escalated assignment is a no-op.
- The UPDATE + outbox insert share a tenant TX; a kill between the
  two rolls both back atomically.

## Assertions

- Exactly one `dms.acknowledgement.reminded.v1` per assignment-per-day,
  regardless of how many times the workflow retries.
- Exactly one `dms.acknowledgement.escalated.v1` per assignment
  lifetime.
- No `acknowledgement_assignments` row has `reminded_count` exceeding
  the number of days the campaign has been active.
- No assignment with `escalated_at` later than `due_at + 7d + clock_skew`.
- Outbox `dms.acknowledgement.reminded.v1` count at end of drill ==
  the count observed by the NATS subscriber. Divergence would mean
  drop-on-the-floor, which is the other failure we care about.

## Setup

- Chaos-mesh CRD: `PodChaos` with `action: pod-kill`, `mode: one`,
  selector `app.kubernetes.io/name=workflow-worker`. Apply once when
  the trigger condition fires (see Verification).
- Seed: one tenant with 500 acknowledgement assignments across three
  active campaigns, sampled so 200 fall within the 3-day remind
  window and 30 fall past the 7-day escalate window.
- Temporal schedule for the tenant is `ack-reminders-<tenant>`
  (registered by `RegisterWave15Schedules`). Trigger the sweep
  on-demand via `temporal schedule trigger --schedule-id ...` rather
  than waiting for 09:00 UTC.

## Verification

1. Start a NATS subscriber that counts messages on
   `dms.acknowledgement.reminded.v1` and `.escalated.v1` for the
   chaos-test tenant.
2. Trigger the sweep. The workflow worker's `AckRemindersWorkflow`
   begins executing `SweepAcknowledgementReminders`.
3. **Trigger condition**: apply the kill when the ack service's
   `/internal/v1/acknowledgement/sweep-reminders` handler has
   written ~50% of its reminder UPDATEs. Watch Postgres log for
   `UPDATE acknowledgement_assignments` on the chaos tenant — apply
   chaos when the row-count crosses 100.
4. Temporal redelivers the activity per its retry policy (see
   `AckRemindersWorkflow` activity options: 3 attempts, 10s / 2x
   / 1m cap). Second run picks up exactly the rows the first run's
   tx rolled back, plus nothing already committed.
5. Once the schedule reports SUCCESS, query:
   ```sql
   SELECT count(*)                              AS reminded_rows,
          sum(reminded_count)                   AS total_reminders,
          count(*) FILTER (WHERE escalated_at IS NOT NULL) AS escalated_rows
     FROM acknowledgement_assignments
    WHERE tenant_id = '<chaos-tenant>';
   ```
   - `reminded_rows` == 200.
   - `total_reminders` == 200 (one per assignment, not two).
   - `escalated_rows` == 30.
6. Check the subscriber: exactly 200 + 30 = 230 messages received.
   Any more → duplicate emission → **FAIL**.
   Any fewer → drop → **FAIL**.

## Known failure modes this exercises

| Mode | Expected behaviour |
|---|---|
| Kill before any UPDATE commits | Whole sweep rolls back; retry is first-run-equivalent; counts match single-run. |
| Kill after partial UPDATE + outbox insert, before tx commit | Both roll back together (shared tx); no partial emission. |
| Kill between outbox insert and NATS publish | Outbox publisher (separate process) picks up on next tick; at-least-once is fine because subscribers dedupe by `nats-msg-id` (UUIDv7 on outbox row). |
| Kill during escalation UPDATE on a row already escalated by a prior run | `escalated_at IS NULL` guard filters it out; no-op. |

## Rollback / cleanup

- `kubectl delete podchaos <name>` if the chaos CRD is still active.
- If the assertions fail, the tenant's `reminded_count` may be
  inflated — fix with:
  ```sql
  UPDATE acknowledgement_assignments
     SET reminded_count = least(reminded_count, extract(day from now() - assigned_at)::int)
   WHERE tenant_id = '<chaos-tenant>';
  ```
- File a `chaos-test-failed` incident referencing this scenario and
  the delta between expected and observed counts.

## Cadence

Run alongside the other 7 scenarios in the quarterly chaos drill
(runbook `docs/runbooks/15-chaos-drill.md`). Add to the checklist
the next time that doc's `scenarios:` section is edited.
