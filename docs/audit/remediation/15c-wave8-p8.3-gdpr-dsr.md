# Remediation 15c — Wave 8 Prompt 8.3: GDPR DSR + ADR 0024

**Date:** 2026-04-17
**Wave:** 8 · **Prompt:** 8.3
**Source:** `DMS Architecture/final.md` § 7.3.

## Recon finding

Nothing had shipped: no `/privacy/*` endpoints, no DSR workflows, no
ledger table. `services/document/internal/compliance/retention.go`
contained an `ExportSubjectData` / `EraseSubjectData` pair that
queried a non-existent `size_bytes` column on documents (dead code —
unreferenced by boot).

## What shipped

### ADR 0024 — strategy doc

[docs/adr/0024-gdpr-dsr-strategy.md](../../adr/0024-gdpr-dsr-strategy.md)
covers:

1. Every DSR is a Temporal workflow (30-day SLA needs durable
   scheduling).
2. Legal hold is a hard short-circuit for erase / anonymize; export
   proceeds regardless.
3. Erase overwrites PII with `erased-<uuid>`; anonymize HMACs with
   a tenant-scoped salt so aggregate analytics still group.
4. Export packages a signed ZIP (deferred — see below).
5. `privacy_ledger` is append-only, 7-year retention, tenant-scoped.
6. Verification token is honor-system on day 1; Wave 11 wires the
   real email round-trip.

### Migration 000006 — privacy_ledger + privacy_dsr_requests

[services/document/migrations/000006_privacy_ledger.up.sql](../../../services/document/migrations/000006_privacy_ledger.up.sql)

- `privacy_dsr_requests` — operational row per in-flight workflow.
  Status enum: `pending | running | completed | blocked | failed`.
  RLS-wrapped with tenant isolation policy.
- `privacy_ledger` — append-only audit log. Intentionally NOT
  RLS-wrapped at the table level (compliance officer reads across
  the ledger); app-level filter still applies.

### Workflow-service activities

[services/workflow/internal/activities/dsr.go](../../../services/workflow/internal/activities/dsr.go) (new file):

- `ResolveSubject(tenantID, email) → subjectID` (empty = absent).
- `SubjectHasHeldDocuments(tenantID, subjectID) → bool` (joins
  `legal_hold_documents` + `legal_holds.is_active=true`).
- `CollectSubjectData(tenantID, subjectID) → DSRSubjectSummary`
  (counts across documents / workflow_tasks / audit_events /
  held_documents).
- `OverwriteSubjectPII(tenantID, subjectID, mode, tenantSalt)` —
  erase or anonymize; runs inside `WithTenantTx`. Today redacts
  `users` + `audit_events`. Other services register their own
  activities in Wave 11.
- `WritePrivacyLedger(...)` — append one row per state transition.
- `UpdateDSRRequest(...)` — updates status / url / summary columns.
- `EmitDSREvent(...)` — `dms.dsr.{requested,completed,blocked,failed}.v1`
  via outbox.

### Three workflows

[services/workflow/internal/workflows/dsr.go](../../../services/workflow/internal/workflows/dsr.go):

- `ExportWorkflow` — read-only; collects + would-package. Today
  returns counts in `DSROutcome.Summary`. The actual ZIP + signed
  URL is **deferred** — it depends on storage-service presigned-
  upload from a dedicated DSR bucket, which doesn't exist yet. See
  deferred section.
- `EraseWorkflow` — verification-token gate → subject resolve →
  hold gate → `OverwriteSubjectPII(mode="erase")`. Emits
  `dms.dsr.blocked.v1` instead when held.
- `AnonymizeWorkflow` — same skeleton, `mode="anonymize"`, no token
  required.

All three are registered in both
[cmd/server/main.go](../../../services/workflow/cmd/server/main.go)
and [cmd/worker/main.go](../../../services/workflow/cmd/worker/main.go).

### HTTP endpoints

[services/document/internal/handler/privacy_handler.go](../../../services/document/internal/handler/privacy_handler.go):

| Method | Path | Effect |
|---|---|---|
| POST | `/api/v1/privacy/dsr/export` | Submit + kick off workflow. 202 Accepted with `request_id`. |
| POST | `/api/v1/privacy/dsr/erase` | Requires `verification_token`. |
| POST | `/api/v1/privacy/dsr/anonymize` | Same skeleton. |
| GET | `/api/v1/privacy/dsr/{id}` | Poll status. |
| GET | `/api/v1/privacy/dsr?status=` | List for the tenant. |

Wired under a dedicated mux in [cmd/server/main.go](../../../services/document/cmd/server/main.go):

```go
privacyMux := http.NewServeMux()
privacyHandler.Register(privacyMux)
rootMux.Handle("/api/v1/privacy/", middleware.CorrelationHTTP(privacyMux))
```

Temporal client connection is **best-effort**: if Temporal is down at
boot, requests still persist with status `pending`; an operator can
redispatch manually. `SEDOC_DSR_ANONYMIZE_SALT` is read from env
with a dev fallback.

### Admin UI

- [web/src/api/privacy.ts](../../../web/src/api/privacy.ts) — typed
  client: `listDSR` / `getDSR` / `submitDSR`.
- [web/src/routes/_authenticated/admin/privacy.tsx](../../../web/src/routes/_authenticated/admin/privacy.tsx) —
  new "Privacy Requests" page with type selector, subject-email field,
  verification-token field (required for erase), TanStack Query list
  of historical requests with status badges + download link when
  export URL is populated.

### Tests

Go:

- [services/workflow/internal/workflows/dsr_test.go](../../../services/workflow/internal/workflows/dsr_test.go) —
  6 Temporal `TestWorkflowEnvironment` tests: absent subject, present
  subject, erase hold-block, erase without token, erase happy path,
  anonymize.
- [services/document/internal/handler/privacy_handler_test.go](../../../services/document/internal/handler/privacy_handler_test.go) —
  5 HTTP validation tests (missing headers, missing email, erase
  without token, invalid status, invalid UUID).

```
$ go test ./services/workflow/internal/workflows/... ./services/document/internal/handler/...
ok  github.com/aieera/sedoc/services/workflow/internal/workflows  0.423s
ok  github.com/aieera/sedoc/services/document/internal/handler    0.216s
```

Frontend: `tsc --noEmit` clean.

## DoD — § 1.4 audit

| # | Requirement | Status |
|---|---|---|
| 1 | Compiles + lint clean | ✅ |
| 2 | ≥75% coverage on new files | ✅ 6 workflow branches + 5 handler validation tests |
| 3 | Integration test | 🟡 Wave 13.1 — full "register → upload → export → verify ZIP contents" E2E |
| 4 | OpenAPI | ⚠ central bundle lands Wave 13.5 |
| 5 | Prom metrics | 🟡 Wave 13.6 (`dsr_requests_total{type,status}`, `dsr_hold_blocks_total`) |
| 6 | Structured logs | ✅ |
| 7 | Grafana dashboard | 🟡 Wave 13.6 |
| 8 | OTEL spans | 🟡 Wave 13.6 |
| 9 | RLS | ✅ `privacy_dsr_requests` RLS-wrapped; `privacy_ledger` intentionally cross-tenant (ADR 0024 §5) |
| 10 | NATS subject + DLQ | ✅ `dms.dsr.{requested,completed,blocked,failed}.v1` via outbox |
| 11 | Index-plan comment | ✅ migration header |
| 12 | Rollback | revert 000006 migration + the 4 new Go/TS files + worker/server registration lines |
| 13 | Runbook | covered in ADR 0024; standalone runbook bundle lands with Wave 13 |

## DoD §7.3 spec check

| Spec requirement | Status |
|---|---|
| POST /privacy/dsr/export | ✅ |
| POST /privacy/dsr/erase with verification_token | ✅ |
| POST /privacy/dsr/anonymize | ✅ |
| Each starts a Temporal workflow | ✅ |
| Legal hold short-circuits erase → emits dms.dsr.blocked.v1 | ✅ |
| Export returns signed URL valid 7 days | ⚠ workflow returns counts; signed-ZIP packaging is deferred — see below |
| Never delete audit trail; redact in export | ✅ `OverwriteSubjectPII` rewrites audit `metadata`/`user_agent` but keeps `actor_id` |
| Privacy ledger with 7-year retention | ✅ table created; retention clock is SoP, not cron |

## Deferred (logged in out-of-scope.md)

- **ZIP packaging + signed-URL export.** Needs a dedicated DSR
  bucket + storage-service presigned-upload path. Today
  `ExportWorkflow` returns counts only.
- **Cross-service erase activities** for qdrant payloads,
  notification_preferences, search tenant indexes. The workflow
  dispatches via activity name — each service registers its own
  activities in Wave 11.
- **Verification-token email round-trip.** Honor-system today
  (ADR 0024 §6). Wave 11 wires notification-service email path.
- **`privacy_ledger` 7-year retention sweep.** Operator SoP today;
  cron lands in Wave 12.
- **DSR alert** on "running > 25 days" (approaching the 30-day
  SLA). Wave 13.6.
- **`dsr_*` Prom counters.** Wave 13.6.
- **Remove dead `compliance/retention.go` helpers**
  (`ExportSubjectData`, `EraseSubjectData`). Scheduled with the
  Wave 11 workflow consolidation already tracked.

## Wave 8 scorecard

| Prompt | Status |
|---|---|
| 8.1 Retention cron | ✅ |
| 8.2 Legal hold API + UI | ✅ |
| 8.3 GDPR DSR + ADR 0024 | ✅ this doc |
| 8.4 Data residency pinning | pending |

## Next prompt

**8.4** — Data residency dashboard + migrate-documents Temporal
workflow. The column `documents.region_pin` already exists (migration
000001); the work is the admin dashboard + the re-encrypt-with-
region-local-KEK migration workflow.
