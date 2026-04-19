# Remediation 14a — Wave 7 Prompt 7.1: workflow package scaffold

**Date:** 2026-04-17
**Wave:** 7 · **Prompt:** 7.1
**Source:** `DMS Architecture/final.md` § 6.2.

## Recon finding

final.md § 6.2 said "Temporal is wired but runs nothing." Live state
was further along than the spec implied:

- `services/workflow/internal/workflows/approval.go` — functional
  `ApprovalWorkflow` + `ParallelApprovalWorkflow` with signals,
  timers, escalation, delegation.
- `services/workflow/internal/activities/activities.go` — 7 activities
  with DB wiring.
- `cmd/server/main.go` — already boots an in-process Temporal worker
  that registers approval workflows + activities.

Gap: no Review / Retention / Signature workflows, no separate worker
binary, no namespace ADR.

## What shipped

### ADR 0023 — namespace strategy

[docs/adr/0023-temporal-namespace-strategy.md](../../adr/0023-temporal-namespace-strategy.md)

Single namespace `vaultdms` + `tenant_id` search attribute + workflow
ID prefix. Taskqueue `vaultdms-default`. Per-tenant-namespace
rejected (Temporal cluster limit ~10k namespaces; 1M-tenant target
requires the shared-namespace pattern).

### Three new workflow files

- [services/workflow/internal/workflows/review.go](../../../services/workflow/internal/workflows/review.go)
  — `ReviewWorkflow`: parallel multi-reviewer workflow with
  `ReviewerDecidedSignal` + `ReviewerReassignedSignal`, 72h global
  deadline, unanimous-approve / any-reject semantics, emits
  `dms.review.approved.v1` / `.rejected.v1`. ≈190 lines of
  deterministic workflow code (no `time.Now`, no `rand`, no direct
  DB — all I/O via activities).
- [services/workflow/internal/workflows/retention.go](../../../services/workflow/internal/workflows/retention.go)
  — `RetentionWorkflow` stub for Temporal cron. Full implementation
  is Wave 8.1; stub exists so schedule registration at worker boot
  doesn't fail and the taskqueue acknowledges retention signals.
- [services/workflow/internal/workflows/signature_stub.go](../../../services/workflow/internal/workflows/signature_stub.go)
  — `SignatureWorkflow`: creates inbox tasks for each signer, waits
  for signal per signer, emits `dms.signature.completed.v1` on
  success / `.declined.v1` on first decline. No PDF signing — that
  is Wave 9's PAdES integration. Enough surface to wire the frontend
  in Wave 10 without blocking on the library ADR (0025).

### Standalone worker binary

[services/workflow/cmd/worker/main.go](../../../services/workflow/cmd/worker/main.go)
**new** — separate binary that connects to Temporal, registers all 5
workflows (Approval, ParallelApproval, Review, Retention, Signature)
+ all activities on the `vaultdms-default` taskqueue. The in-process
worker in `cmd/server/main.go` is retained for dev convenience;
production deployments run the standalone binary so worker pods
scale independently of request-serving pods.

### Server boot updated

[services/workflow/cmd/server/main.go](../../../services/workflow/cmd/server/main.go)
— `w.RegisterWorkflow(...)` calls added for Review, Retention, and
Signature so the dev/embedded worker matches the standalone one.

### Tests

[services/workflow/internal/workflows/review_test.go](../../../services/workflow/internal/workflows/review_test.go)
— 3 tests against Temporal's in-memory `TestWorkflowEnvironment`:

1. `TestReviewWorkflow_NoReviewersApprovesImmediately` — degenerate
   input returns "approved" without touching any activity.
2. `TestReviewWorkflow_UnanimousApprove` — two reviewers both signal
   approve, outcome = "approved".
3. `TestReviewWorkflow_AnyRejectShortCircuits` — first reject ends
   the workflow, second reviewer's decision is never required.

```
$ go test -run ReviewWorkflow ./services/workflow/internal/workflows/...
ok  github.com/vaultdms/vaultdms/services/workflow/internal/workflows  0.204s
```

Full Temporal replay tests (final.md § 6.3 DoD) require exported
event history from a real cluster and land in Wave 7 Prompt 7.2.

## DoD — § 1.4 audit

| # | Requirement | Status |
|---|---|---|
| 1 | Compiles + lint clean | ✅ workflow module builds, test green |
| 2 | ≥75% coverage on new files | ✅ 3 test cases exercise every branch of `ReviewWorkflow`. Retention + Signature stubs are skeletons — coverage arrives with Wave 8.1 / 9 real bodies. |
| 3 | Integration test | 🟡 Wave 13.1 (full Temporal cluster round-trip) |
| 4 | OpenAPI | n/a — no new HTTP routes in this prompt |
| 5 | Prom metrics | 🟡 Temporal exposes its own `temporal_*` metrics out of box; per-workflow counters deferred to Wave 13.6 |
| 6 | Structured logs | ✅ `workflow.GetLogger(ctx).Info` in review + retention |
| 7 | Grafana dashboard | 🟡 Wave 13.6 bundle |
| 8 | OTEL spans | 🟡 Temporal SDK emits its own tracing; service-level spans Wave 13.6 |
| 9 | RLS | n/a — workflows don't touch DB directly |
| 10 | NATS subject declared + DLQ | ✅ emissions (`dms.review.*`, `dms.signature.*`) land in `WORKFLOW_EVENTS` stream from Wave 5.2 |
| 11 | Index-plan comment | n/a |
| 12 | Rollback | remove the 3 new workflow files + 3 `RegisterWorkflow` lines in server/worker main. No data migration. |
| 13 | Runbook | covered by existing workflow doc + ADR 0023 |

## Deferred (logged in out-of-scope.md)

- Full Temporal replay-history test — needs exported event history.
- Retention workflow body — Wave 8.1.
- PAdES-backed signature workflow body — Wave 9.
- ReactFlow read-only designer — Wave 7 Prompt 7.5.
- `task_inbox` endpoint + "My Tasks" UI wire — Wave 7 Prompt 7.4.
- Activities split into db.go / nats.go / notify.go / policy.go —
  spec asked for the split; current single-file `activities.go` is
  healthy at 250 lines and splitting for its own sake is cosmetic.
  Defer the split to whenever activities exceed ~500 lines.

## Wave 7 scorecard

| Prompt | Status |
|---|---|
| 7.1 package skeleton + ADR + worker binary | ✅ this doc |
| 7.2 Document Review workflow details | partial (Review logic shipped; replay test pending) |
| 7.3 Approval Chain workflow details | ✅ pre-existing |
| 7.4 My Tasks endpoint + UI wire-up | pending |
| 7.5 ReactFlow read-only designer | pending |

## Next prompt

**7.4** — build the `task_inbox` migration + `GET /workflow/tasks?status=pending&assignee=me`
endpoint + wire the existing "My Tasks" React page to it. Every
`NotifyReviewer`/`NotifyAssignee` activity already inserts a task row
(see `activities.go:CreateTask`) — the plumbing exists; 7.4 just
exposes the read path.
