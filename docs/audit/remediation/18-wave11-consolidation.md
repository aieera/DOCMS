# Remediation 18 — Wave 11 Integration & Plumbing (consolidation pass)

**Date:** 2026-04-18
**Wave:** 11 · three follow-ups from Waves 8 + 9 + 10 closed in one pass.

## Scope

Wave 11 is a consolidation wave — accumulated out-of-scope items
from Waves 8.1–10.9 that share the "security / correctness hardening"
theme. This pass ships three: workflow RLS audit, OPA compliance-
officer gate, dead-code removal.

## 11.1 Workflow service RLS audit

### Recon

Pre-Wave-11 `services/workflow` ran tenant-scoped queries against
the raw `a.Pool` / `r.pool` — the `SET LOCAL app.current_tenant`
GUC was never set and Postgres RLS policies like

```sql
USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
```

evaluated `current_setting` as an empty string, silently failing
open for the application role. Queries still filtered by
`tenant_id = $N` in the WHERE clause, so cross-tenant leakage was
unlikely in practice, but the second line of defence was off.

### What changed

- **New `(a *Activities) runTenant(ctx, tenantID, fn)` helper** in
  [services/workflow/internal/activities/tenant.go](../../../services/workflow/internal/activities/tenant.go).
  Parses the string tenant id, opens a tenant-scoped tx via
  `database.WithTenantTx`. Matching
  `(r *Repository) runTenant` in
  [services/workflow/internal/repository/repository.go](../../../services/workflow/internal/repository/repository.go).
- **Every tenant-touching query** in the workflow service now
  routes through `runTenant`. Refactored files:
  - `activities/activities.go` — CreateTask, CompleteTask,
    DelegateTask, SetDocumentLifecycle, EvaluateCondition
  - `activities/dsr.go` — ResolveSubject, SubjectHasHeldDocuments,
    CollectSubjectData, WritePrivacyLedger, UpdateDSRRequest
  - `activities/retention.go` — SweepExpiredRetentions, DocumentOnLegalHold
  - `activities/residency.go` — NextPendingMigrationDoc,
    FinalizeMigration, MarkItemFailed, QueryResidencyStats
  - `repository/repository.go` — every Definition / Instance /
    Task method
- **Signature changes** — NextPendingMigrationDoc and MarkItemFailed
  gained a `tenantID` first arg (previously operated on
  migration-id only). `ResidencyMigrationWorkflow` updated to pass
  it; corresponding test stubs updated.

Activities that already used `WithTenantTx` (NotifyAssignee,
PublishEvent, OverwriteSubjectPII, RetentionTransition,
EmitRetentionEvent, EmitDSREvent, MoveDocumentRegion, EnumerateDocsForMigration)
were untouched.

### Tests

All existing workflow tests updated for the new activity
signatures. `go test ./services/workflow/...` passes.

## 11.2 OPA compliance-officer gate on legal holds

### Recon

Spec §7.2: *"OPA policy: only users with role=compliance_officer can
create/release holds."* The hold handler read
`X-Tenant-ID` / `X-User-ID` headers but never checked the caller's
role.

### What changed

- **`requireRole(w, r, allowed...)` helper** in
  [services/document/internal/handler/compliance_handler.go](../../../services/document/internal/handler/compliance_handler.go).
  Reads `X-User-Role` from the request. Returns 403 Forbidden with
  the role name + required roles list on mismatch.
- **`create`, `release`, `update` hold endpoints** now call
  `requireRole(w, r, "compliance_officer", "admin", "owner")`
  before any business logic. Org admin + owner accepted because
  they inherit admin capability through policy.rego rule 6.
  `get` and `list` remain open (read-only).
- **Frontend propagation**: [web/src/api/client.ts](../../../web/src/api/client.ts)
  axios interceptor now sets `X-User-ID` and `X-User-Role` from
  the auth store on every request. The other admin-surface
  handlers that previously read these headers continue to work
  unchanged; the new role header flows through.

### Tests

- 8 pre-existing compliance handler tests updated: every
  authenticated request sets `X-User-Role: compliance_officer`.
- New test `TestHolds_Create_Forbidden403_WhenNotComplianceOfficer`
  asserts a caller with `role=member` is rejected with 403 even
  with a valid body. Validates the gate fires.

```
$ go test ./services/document/internal/handler/...
ok  github.com/vaultdms/vaultdms/services/document/internal/handler  0.271s
```

## 11.3 Dead-code removal — `compliance/retention.go`

### Recon

`services/document/internal/compliance/retention.go` contained:

- `RetentionEnforcer` (goroutine cron) — superseded by Wave 8.1
  Temporal `RetentionWorkflow` + per-tenant schedule bootstrap.
- `CreateLegalHold` / `ReleaseLegalHold` — queried
  non-existent columns (`legal_hold_items`, `reason`, `status`).
  Superseded by Wave 8.2 `HoldsService`.
- `GetResidencyStats` — superseded by Wave 8.4 `ResidencyHandler`.
- `ExportSubjectData` / `EraseSubjectData` — superseded by Wave 8.3
  DSR workflows (`ExportWorkflow` / `EraseWorkflow`).

No production code path referenced any of them.

### What changed

- File deleted. Package comment lives in `holds.go` (Wave 8.2).
- `go build ./services/document/...` clean; all document tests still pass.

## Deferred (remaining Wave 11 items)

Logged in out-of-scope for follow-up prompts:

- Cross-service DSR erase activities (Wave 8.3 deferral). Still
  waits for each service to register its own Temporal activity
  name with a shared signature contract.
- Notification service transactional email path (DSR verification
  token round trip, also Wave 8.3).
- Redaction endpoint + hold gate (Wave 8.2).
- Per-region KEK aliases (Wave 8.4).
- Signer service Java DSS sidecar (Wave 9.2b).

## Wave 11 scorecard (part 1)

| Item | Status |
|---|---|
| **Workflow RLS audit** | ✅ this doc |
| **OPA `compliance_officer` gate** | ✅ this doc |
| **Dead-code cleanup** | ✅ this doc |
| Cross-service DSR erase activities | pending |
| Notification transactional email (DSR token) | pending |
| Redaction endpoint + hold gate | pending |
| Per-region KEK aliases | pending |

## Next prompt

Wave 11 has 4 items pending. Biggest pilot-unblock is probably the
**notification transactional email path** — DSR erase today accepts
any non-empty verification token (ADR 0024 §6 "honor system"), and
wiring the real email → token → redeem loop closes a documented
security gap.

Alternatively: **redaction endpoint** (pilot customers asked for it
in the GDPR conversations) or **per-region KEK aliases** (unblocks
multi-region deployments).
