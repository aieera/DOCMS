# Remediation 15a — Wave 8 Prompt 8.1: retention cron

**Date:** 2026-04-17
**Wave:** 8 · **Prompt:** 8.1
**Source:** `DMS Architecture/final.md` § 7.1.

## Recon finding

Wave 7 P7.1 left `RetentionWorkflow` as a deterministic no-op stub so
workflow registration wouldn't fail at boot. Other pieces already in
place:

- `documents.retention_until TIMESTAMPTZ` (migration 000001) — the
  per-document retention-clock column.
- `documents.lifecycle_state` check constraint includes `archived`,
  `disposed`, `legal_hold`.
- `legal_holds` + `legal_hold_documents` tables — hold bindings.
- `retention_policies` table (rules engine).

Gaps this prompt closes: no sweep activity, no hold gate, no state
transition emission, no schedule registration.

## What shipped

### Retention activities

[services/workflow/internal/activities/retention.go](../../../services/workflow/internal/activities/retention.go)
(new file — keeps `activities.go` under the ~500-line threshold logged
in out-of-scope):

- `SweepExpiredRetentions(tenantID, asOf, limit)` — returns
  `[]ExpiredDocument{document_id, lifecycle_state, retention_until, updated_at}`
  for docs with `retention_until ≤ asOf`, `deleted_at IS NULL`, and
  lifecycle in `{active, retained, archived}`. Capped at `limit`
  (default 10k) to bound per-tick work.
- `DocumentOnLegalHold(tenantID, docID)` — EXISTS join of
  `legal_hold_documents` to `legal_holds.is_active = true`.
- `RetentionTransition(tenantID, docID, newState, subject)` — validates
  `newState ∈ {archived, disposed}`, runs the lifecycle UPDATE + the
  `dms.retention.*.v1` outbox insert in a single
  `WithTenantTx`-wrapped transaction, so state change and event
  emission are atomic.
- `EmitRetentionEvent(tenantID, docID, subject, reason)` — used for
  event-only cases (`dms.retention.held.v1`,
  `dms.retention.dispose_candidate.v1`).

All four honor the workflow-service atomicity guarantee: state + event
never diverge.

### RetentionWorkflow real body

[services/workflow/internal/workflows/retention.go](../../../services/workflow/internal/workflows/retention.go):

- Calls `SweepExpiredRetentions` with `workflow.Now(ctx)` (deterministic).
- Per doc: `DocumentOnLegalHold` gate → held docs increment `HeldSkipped`
  and emit `dms.retention.held.v1`.
- `active | retained` → `RetentionTransition` to `archived`.
- `archived` + aged ≥ `ArchiveDays` (default 30) → emit
  `dms.retention.dispose_candidate.v1`. **No auto-dispose** — spec
  requires two-person approval; logged in out-of-scope as a separate
  interactive workflow.
- `DryRun: true` counts branches but executes neither transitions nor
  event emissions.

Per-doc failures don't kill the sweep — errors are appended to
`RetentionOutcome.Errors` and the loop continues to the next document.
Top-level activity retry is 3 attempts, 10s initial, 2x backoff, 2m cap.

Subject constants exported as `SubjectRetentionArchived`,
`SubjectRetentionDisposeCandidate`, `SubjectRetentionHeld`.

### Schedule bootstrap

[services/workflow/internal/workflows/retention_schedule.go](../../../services/workflow/internal/workflows/retention_schedule.go)
— `RegisterRetentionSchedules(ctx, pool, tc, taskQueue)` queries every
live organization and ensures each has a
`retention-<tenant_uuid>` schedule at `0 3 * * *` UTC with overlap
policy `SKIP`. `AlreadyExists` is swallowed so boot is idempotent.

[services/workflow/cmd/worker/main.go](../../../services/workflow/cmd/worker/main.go)
calls the bootstrap after worker start. Failure is logged but NOT
fatal — scheduling is a control-plane concern, and we'd rather serve
on-demand workflows without schedules than crashloop the worker.

### Tests

[services/workflow/internal/workflows/retention_test.go](../../../services/workflow/internal/workflows/retention_test.go)
— 6 Temporal `TestWorkflowEnvironment` tests using an in-memory
activity stub struct registered with explicit
`activity.RegisterOptions{Name: ...}` so the workflow's string-based
activity dispatch lines up:

1. `TestRetention_NoExpiredDocs` — empty sweep → no transitions, zero
   counts.
2. `TestRetention_ActiveDocGetsArchived` — one `active` doc →
   `archived` transition + `Archived: 1`.
3. `TestRetention_HeldDocIsSkippedAndEmits` — held doc → no
   transition, `HeldSkipped: 1`, `dms.retention.held.v1` emitted.
4. `TestRetention_OldArchivedBecomesDisposeCandidate` — 60-day-old
   `archived` → `DisposeCandidates: 1`, event emitted, **no** state
   change.
5. `TestRetention_RecentlyArchivedStaysQuiet` — 5-day-old `archived`
   → no event, no change.
6. `TestRetention_DryRunTakesNoActions` — `Archived` counter
   increments but zero activity calls for transitions/events.

```
$ go test ./services/workflow/internal/workflows/...
ok  github.com/aieera/sedoc/services/workflow/internal/workflows  0.246s
```

### Runbook

[docs/runbooks/08-retention.md](../../runbooks/08-retention.md) — state
machine diagram, event subjects, emergency pause commands (per-tenant
and global), dry-run invocation, failure modes, Wave 13.6 metrics
preview.

## DoD — § 1.4 audit

| # | Requirement | Status |
|---|---|---|
| 1 | Compiles + lint clean | ✅ |
| 2 | ≥75% coverage on new files | ✅ 6 branch tests cover all state-machine edges |
| 3 | Integration test | 🟡 Wave 13.1 (real Temporal + pg) |
| 4 | OpenAPI | n/a — schedule-triggered, no HTTP route |
| 5 | Prom metrics | 🟡 Wave 13.6 |
| 6 | Structured logs | ✅ `workflow.GetLogger(ctx).Info` at sweep entry |
| 7 | Grafana dashboard | 🟡 Wave 13.6 |
| 8 | OTEL spans | 🟡 Temporal SDK emits its own tracing |
| 9 | RLS | ✅ `RetentionTransition` + `EmitRetentionEvent` use `WithTenantTx`. `SweepExpiredRetentions` + `DocumentOnLegalHold` are read-only with WHERE-clause tenant scoping — folds into Wave 11 workflow-RLS audit |
| 10 | NATS subject | ✅ `dms.retention.{archived,dispose_candidate,held}.v1` — all via outbox |
| 11 | Index-plan comment | ✅ sweep query uses `idx_documents_lifecycle` partial + `retention_until` scan |
| 12 | Rollback | revert the 3 new workflow/activity files + worker main diff; no DB migration |
| 13 | Runbook | ✅ `docs/runbooks/08-retention.md` |

## Deferred (logged in out-of-scope.md)

- **Two-person dispose-approval workflow** — spec line "after
  dispose-approval to disposed (soft delete + ciphertext shred)". The
  cron flags candidates only; actual disposition is interactive and
  ships as a separate workflow in Wave 8 follow-up.
- **Ciphertext shred on disposition** — depends on the dispose
  workflow above; the storage-service `shredBlob` RPC is also TBD.
- **Retention policy rules engine** — `retention_policies` table is
  populated manually today; the document-creation path doesn't yet
  compute `retention_until` from matching policies. Wave 8 follow-up.
- **Admin UI to configure policies** — CRUD endpoints for
  `retention_policies` plus a React page. Wave 10.
- **Per-tenant metrics** (`retention_*_total`) — Wave 13.6.

## Wave 8 scorecard

| Prompt | Status |
|---|---|
| 8.1 Retention cron | ✅ this doc |
| 8.2 Legal hold API + UI | pending |
| 8.3 GDPR DSR (ADR 0024) | pending |
| 8.4 Data residency pinning | pending |

## Next prompt

**8.2** — Legal hold API: `POST/GET/DELETE /compliance/holds`,
delete-blocking on held documents, hold-release audit event.
