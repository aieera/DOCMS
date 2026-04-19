# Retention cron — operator runbook

Wave 8 Prompt 8.1. The retention cron is a per-tenant Temporal
schedule that sweeps documents whose `retention_until` has passed
and transitions them down the lifecycle state machine.

## Architecture

- Schedule id: `retention-<tenant_uuid>`.
- Cron: `0 3 * * *` UTC (daily 03:00 UTC).
- Workflow: `RetentionWorkflow` on taskqueue `vaultdms-default`.
- Overlap policy: `SKIP` — if a previous run is still in flight, the
  tick is dropped. We prefer a missed cron tick over two concurrent
  retention sweeps on the same tenant.
- Namespace: `vaultdms` (ADR 0023).

Schedules are bootstrapped on every worker start via
`RegisterRetentionSchedules`. The bootstrap is idempotent — existing
schedules aren't re-created.

## State machine

```
active | retained
     │
     ▼  (retention_until ≤ now, no legal hold)
  archived   ──┐
     │        │
     │ ≥ ArchiveDays (default 30d)
     ▼        │
  dispose_candidate   (event only; no state change — admin approves separately)
     │
     ▼  (two-person approval workflow — Wave 8 follow-up)
  disposed  (soft delete + ciphertext shred)
```

## Events

| Subject | Meaning |
|---|---|
| `dms.retention.archived.v1` | Document transitioned `active|retained → archived`. |
| `dms.retention.dispose_candidate.v1` | Archived long enough to be eligible for disposition. Admin action required. |
| `dms.retention.held.v1` | Retention tried to act but the document has an active legal hold. |

All subjects are emitted through the outbox — durability is guaranteed
even if NATS is briefly unavailable.

## Emergency pause

To stop one tenant's schedule:

```bash
temporal schedule pause --schedule-id retention-<tenant_uuid> \
  --reason "p1-incident 2026-04-17"
```

To stop all schedules (rare — prefer per-tenant):

```bash
for id in $(temporal schedule list --query 'retention-%' -o json | jq -r '.[].id'); do
  temporal schedule pause --schedule-id "$id" --reason "global-pause"
done
```

Resume with `temporal schedule unpause --schedule-id <id>`.

## Dry-run

Inspect what a cron tick would do without mutating state or emitting
events:

```bash
temporal workflow start \
  --workflow-id retention-dryrun-<tenant>-$(date +%s) \
  --task-queue vaultdms-default \
  --type RetentionWorkflow \
  --input '{"tenant_id":"<tenant_uuid>","dry_run":true}'
```

Returns a `RetentionOutcome` JSON with `archived`, `dispose_candidates`,
`held_skipped` counts. No transitions, no events.

## Failure modes

- **Sweep activity fails** — workflow returns error, Temporal retries
  per the workflow-level retry policy (max 3 attempts, 10s initial,
  2x backoff). After 3 failures the schedule logs an error and
  proceeds to the next tick.
- **Single-document transition fails** — workflow continues to the
  next document and appends to `RetentionOutcome.Errors`. The next
  tick will retry.
- **Orphan schedule** (tenant de-provisioned) — workflow runs against
  an empty sweep and no-ops. De-provisioning cleanup ships with
  Wave 12.3 (CMK scheduled-deletion workflow).

## Metrics (Wave 13.6)

Per-tenant counters will land in Wave 13.6:

- `retention_documents_archived_total{tenant}`
- `retention_held_skipped_total{tenant}`
- `retention_dispose_candidates_total{tenant}`
- `retention_errors_total{tenant,phase}`

For now, check `RetentionOutcome` in the schedule's run history.
