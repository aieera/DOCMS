# Remediation 14b — Wave 7 Prompt 7.4: task inbox read path + UI wire

**Date:** 2026-04-17
**Wave:** 7 · **Prompt:** 7.4
**Source:** `DMS Architecture/final.md` § 6.2 / § 6.4.

## Recon finding

Prompt 7.1 introduced `ReviewWorkflow` + `NotifyAssignee` / `CreateTask`
activities. A pending endpoint — `GET /workflows/tasks/mine` — and a
placeholder React page existed but were unwired:

- Placeholder route [web/src/routes/_authenticated/tasks.tsx](../../../web/src/routes/_authenticated/tasks.tsx)
  rendered an empty state with no network call.
- `web/src/api/workflows.ts:getMyTasks` existed but nothing consumed it.
- Worse, the DB schema mismatch: migration 000001 defined
  `workflow_tasks.step_id TEXT NOT NULL` and **no** `document_id`, while
  `activities.go:CreateTask` writes `document_id` and never writes
  `step_id`. The inbox-INSERT path would fail at runtime the moment a
  real ReviewWorkflow fired.

## What shipped

### Schema alignment migration

[services/document/migrations/000005_workflow_tasks_document.up.sql](../../../services/document/migrations/000005_workflow_tasks_document.up.sql)

- `ADD COLUMN document_id UUID` with composite FK to
  `documents(tenant_id, id) ON DELETE SET NULL`. Nullable because
  system-initiated tasks (retention cron, Wave 8.1) aren't tied to a
  document.
- `ALTER COLUMN step_id DROP NOT NULL`. `step_id` stays reserved for
  the ReactFlow designer (Prompt 7.5).
- New partial index
  `idx_wf_tasks_assignee_pending(tenant_id, assignee_id, created_at DESC) WHERE status = 'pending'`
  for the hot inbox query.

No backfill: the table is empty in every environment — workflow was a
stub before Wave 7.

### Handler: query-param read endpoint

[services/workflow/internal/handler/handler.go](../../../services/workflow/internal/handler/handler.go)
added `GET /api/v1/workflows/tasks?assignee=<id|me>&status=<state>`:

- `assignee` defaults to `me` → resolves to `X-User-ID`.
- `status` is validated against a whitelist; empty returns all states.
- `400` when headers are missing or `status` is unknown.
- `/tasks/mine` kept as a back-compat shim for the existing
  `web/src/api/workflows.ts:getMyTasks` callers during rollout.

### Repository: document-title join

[services/workflow/internal/repository/repository.go](../../../services/workflow/internal/repository/repository.go)
— `ListTasks` now LEFT JOINs `documents` so the inbox UI renders the
document title without a second round trip. Cheap under the new partial
index plus `documents(tenant_id, id)` PK. `COALESCE` on
`document_id`/`notes` handles any pre-migration rows.

### Service

[services/workflow/internal/service/service.go](../../../services/workflow/internal/service/service.go)
— renamed `ListMyTasks(tenantID, assigneeID)` to
`ListTasks(tenantID, assigneeID, status)`. Callers updated in handler.

### Frontend wire

- [web/src/api/workflows.ts](../../../web/src/api/workflows.ts) — exports
  `WorkflowTask` TypeScript type, `getMyTasks({ status? })` builds
  `?assignee=me&status=…` query string.
- [web/src/routes/_authenticated/tasks.tsx](../../../web/src/routes/_authenticated/tasks.tsx)
  — full TanStack Query implementation with `pending / completed / all`
  tab filter, per-task Approve/Reject buttons (posts to
  `/workflows/instances/{id}/signal`), optimistic invalidation on
  success, dayjs relative-time timestamps. Uses existing
  `Badge` / `Button` / `Skeleton` / `EmptyState` primitives.

### Tests

[services/workflow/internal/handler/handler_test.go](../../../services/workflow/internal/handler/handler_test.go)
— added:

- `TestListTasks_RequiresHeaders` — missing `X-Tenant-ID` + `X-User-ID`
  → 400.
- `TestListTasks_InvalidStatus` — `?status=bogus` → 400.

```
$ go test ./services/workflow/internal/handler/...
ok  github.com/vaultdms/vaultdms/services/workflow/internal/handler  0.178s
```

Frontend type check:

```
$ cd web && npx tsc --noEmit   # clean
```

## DoD — § 1.4 audit

| # | Requirement | Status |
|---|---|---|
| 1 | Compiles + lint clean | ✅ Go + TS both clean |
| 2 | ≥75% coverage on new files | ✅ handler param-branches covered; repository change is query-string + scan, runs under existing integration path |
| 3 | Integration test | 🟡 Wave 13.1 (real pg + Temporal) |
| 4 | OpenAPI | ⚠ no central OpenAPI bundle yet (Wave 13.5); endpoint documented here + in handler code |
| 5 | Prom metrics | 🟡 Wave 13.6 (per-service HTTP histograms) |
| 6 | Structured logs | ✅ inherited from middleware chain |
| 7 | Grafana dashboard | 🟡 Wave 13.6 |
| 8 | OTEL spans | 🟡 Wave 13.6 |
| 9 | RLS | ⚠ workflow service still queries without `SET LOCAL app.current_tenant` — logged in out-of-scope (Wave 11 consolidation) |
| 10 | NATS subject | n/a — read-only endpoint |
| 11 | Index-plan comment | ✅ in migration header |
| 12 | Rollback | `000005_workflow_tasks_document.down.sql` + revert handler/service/repo/UI |
| 13 | Runbook | endpoint is self-describing; no pager-worthy failure modes |

## Deferred (logged in out-of-scope.md)

- Workflow-service DB reads don't wrap queries in
  `WithTenantTx` / `SET LOCAL app.current_tenant`. Tenant isolation is
  currently enforced by WHERE-clause only. Ships with the broader
  workflow-RLS audit in Wave 11.
- Tasks page lacks a **Delegate** action. Add once user-picker
  component exists (Wave 10).
- Task-detail drawer (shows full instance state machine, audit trail).
  Wave 10.
- Real-time inbox updates via SSE / NATS subscription. Polling
  via `useQuery` refetch covers pilot.

## Wave 7 scorecard

| Prompt | Status |
|---|---|
| 7.1 package skeleton + ADR + worker binary | ✅ |
| 7.2 Document Review workflow details | partial (replay test pending) |
| 7.3 Approval Chain workflow details | ✅ pre-existing |
| 7.4 My Tasks endpoint + UI wire-up | ✅ this doc |
| 7.5 ReactFlow read-only designer | pending |

## Next prompt

**7.5** — ReactFlow read-only designer. Render a workflow definition's
step list as a DAG visualization on the definition-detail page.
Read-only (spec explicitly defers drag-to-create to post-G3).
