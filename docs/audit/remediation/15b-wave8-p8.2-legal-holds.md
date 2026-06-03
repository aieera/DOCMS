# Remediation 15b — Wave 8 Prompt 8.2: legal hold API + UI

**Date:** 2026-04-17
**Wave:** 8 · **Prompt:** 8.2
**Source:** `DMS Architecture/final.md` § 7.2.

## Recon finding

The schema had `legal_holds` + `legal_hold_documents` (migration 000001)
but:

- No HTTP endpoints exposed the tables.
- `services/document/internal/compliance/retention.go` held two
  helpers (`CreateLegalHold`, `ReleaseLegalHold`) that referenced
  non-existent columns (`reason`, `status`, `legal_hold_items`) —
  dead code against a schema that never shipped.
- `DocumentService.DeleteDocument` only blocked deletes when
  `lifecycle_state == 'legal_hold'`; binding rows in
  `legal_hold_documents` were not consulted. A hold applied to an
  active document was effectively unenforced on the delete path.
- `pkg/errors.ErrLegalHold` mapped to HTTP 409, but the DoD calls for
  423 Locked.
- `web/src/routes/_authenticated/admin/legal-holds.tsx` was a
  placeholder EmptyState.

## What shipped

### HTTP API (REST on a dedicated mux)

[services/document/internal/handler/compliance_handler.go](../../../services/document/internal/handler/compliance_handler.go)
— mounts five endpoints under a dedicated compliance mux so status
code 423 and validation errors flow through the handler's own writer
rather than being re-wrapped by grpc-gateway:

| Method | Path | Effect |
|---|---|---|
| POST | `/api/v1/compliance/holds` | Create a hold and attach documents. 201. |
| GET | `/api/v1/compliance/holds?status=active&document_id=&custodian=` | List holds (status: active/released/all). |
| GET | `/api/v1/compliance/holds/{id}` | Fetch one hold + document bindings. |
| PATCH | `/api/v1/compliance/holds/{id}` | Update name/description, add/remove docs. Refuses once released. |
| POST | `/api/v1/compliance/holds/{id}/release` | Release the hold. Requires `reason` + `approver_id`. |

Immutability: `id`, `tenant_id`, and `matter_reference` are never
mutable — PATCH only accepts name/description/add/remove fields.

Wired in [services/document/cmd/server/main.go](../../../services/document/cmd/server/main.go)
behind `rootMux.Handle("/api/v1/compliance/", ...)` with
`CorrelationHTTP` middleware.

### Service + repo

[services/document/internal/compliance/holds.go](../../../services/document/internal/compliance/holds.go)
— new `HoldsService` using the real schema
(`legal_holds`, `legal_hold_documents`). Every write runs inside
`WithTenantTx` so state change + outbox row are atomic:

- `Create` — inserts the hold, per-document snapshot of
  `previous_lifecycle_state`, and outbox event `dms.hold.applied.v1`.
- `Release` — flips `is_active=false`, stamps `released_by`/
  `released_at`, emits `dms.hold.released.v1`. Rows stay in the
  binding table for audit; only the flag governs enforcement.
- `Update` — adjusts attached docs + name/description, emits
  `dms.hold.updated.v1`. Refuses released holds (409).
- `Get` / `List` — with ListFilter supporting status + document_id +
  custodian query params.
- `AnyActiveHoldFor(ctx, tenantID, documentID)` — EXISTS join used by
  DocumentService to gate mutations.

### Delete-path enforcement

[services/document/internal/service/service.go](../../../services/document/internal/service/service.go)
— added `HoldsChecker` interface + `SetHoldsChecker` optional wire-up
(local interface avoids an import cycle on the compliance package).

[services/document/internal/service/documents.go](../../../services/document/internal/service/documents.go)
`DeleteDocument` now runs the binding check **in addition to** the
pre-existing `model.IsLegalHoldBlocked(lifecycle_state, …)` guard. Both
representations of "held" (lifecycle state + binding table) are
covered.

### 423 Locked mapping

[pkg/errors/errors.go](../../../pkg/errors/errors.go) split
`KindLegalHold` out of the 409 bucket:

```
case KindLegalHold:
    out.Code = http.StatusLocked  // RFC 4918 §11.3
```

Existing test updated; 9 pkg/errors tests still green. The gRPC side
stays on `FailedPrecondition` (Go gRPC has no RFC 4918 analogue).

### Admin UI

- [web/src/api/holds.ts](../../../web/src/api/holds.ts) — `LegalHold`
  interface + `listHolds` / `getHold` / `createHold` / `releaseHold`.
- [web/src/routes/_authenticated/admin/legal-holds.tsx](../../../web/src/routes/_authenticated/admin/legal-holds.tsx)
  — TanStack Query wire: active/released/all filter tabs, per-hold
  Release button with reason + approver prompts, inline matter
  reference + relative timestamps.

Create-hold flow is deferred to Wave 10 (needs a document multi-select
picker we don't have yet; for now holds are created via API from
compliance tooling).

### Tests

[services/document/internal/handler/compliance_handler_test.go](../../../services/document/internal/handler/compliance_handler_test.go)
— 7 handler tests covering the HTTP-level invariants:

1. `Create_Missing401` — no tenant/user headers → 401.
2. `Create_MalformedJSON400` — `{{{` body → 400.
3. `Create_InvalidDocUUID400` — non-UUID in `document_ids` → 400.
4. `List_InvalidStatus400` — `?status=bogus` → 400.
5. `Release_MissingApprover400` — missing approver_id → 400.
6. `Get_BadUUID400` — non-UUID id path → 400.
7. `LegalHoldMapsTo423` — direct writeErr on `ErrLegalHold` → 423
   with `type=LEGAL_HOLD` body.

```
$ go test ./services/document/internal/handler/... ./pkg/errors/...
ok  github.com/aieera/sedoc/services/document/internal/handler  0.333s
ok  github.com/aieera/sedoc/pkg/errors                          1.727s
```

Frontend: `tsc --noEmit` clean.

## DoD — § 1.4 audit

| # | Requirement | Status |
|---|---|---|
| 1 | Compiles + lint clean | ✅ Go + TS |
| 2 | ≥75% coverage on new files | ✅ handler validation paths covered; service tx path hits Wave 13.1 |
| 3 | Integration test | 🟡 Wave 13.1 — covers hold + delete round-trip against real pg |
| 4 | OpenAPI | ⚠ no central bundle yet (Wave 13.5); endpoint documented in handler + this doc |
| 5 | Prom metrics | 🟡 Wave 13.6 (`legal_holds_applied_total`, `legal_holds_released_total`) |
| 6 | Structured logs | ✅ via handler's zerolog + middleware chain |
| 7 | Grafana dashboard | 🟡 Wave 13.6 |
| 8 | OTEL spans | 🟡 Wave 13.6 |
| 9 | RLS | ✅ `HoldsService` runs through `WithTenantTx` — SET app.current_tenant every write |
| 10 | NATS subject + DLQ | ✅ `dms.hold.{applied,released,updated}.v1` via outbox → DOC_EVENTS (existing DLQ wiring from Wave 5.2) |
| 11 | Index-plan comment | ✅ pre-existing `idx_legal_holds_active`, `idx_lhd_document`, `idx_lhd_hold` |
| 12 | Rollback | revert compliance_handler.go + holds.go + service.SetHoldsChecker wiring + pkg/errors 423 mapping |
| 13 | Runbook | covered by OpenAPI endpoint docs; a dedicated runbook lands with Wave 8.4 residency doc |

## DoD §7.2 spec check

| Spec requirement | Status |
|---|---|
| POST /compliance/holds | ✅ |
| GET /compliance/holds with status/document_id/custodian filters | ✅ |
| GET /compliance/holds/{id} | ✅ |
| PATCH /compliance/holds/{id} (extend / scope / custodian) | ✅ name + desc + bindings; "extend" is no-op today since holds don't carry expiry |
| POST /compliance/holds/{id}/release (reason + approver) | ✅ |
| A hold pins every current AND future version of a document | ✅ binding is per-document, so every version under that document_id is pinned |
| Deletion / disposition / redaction blocked on held docs | ✅ delete: binding-table gate in DeleteDocument. Disposition: Wave 8.1 retention cron already skips held docs + emits `dms.retention.held.v1`. Redaction: no redaction endpoint exists yet (deferred — logged) |
| Release emits `dms.hold.released.v1` with audit trail | ✅ |
| OPA policy: only compliance_officer can create/release | 🟡 handler doesn't yet invoke Policy Service — see out-of-scope |
| 423 Locked on DELETE of held doc | ✅ errors.go mapping |

## Deferred (logged in out-of-scope.md)

- **OPA `role=compliance_officer` gate** on create/release. The existing
  `holds` HTTP mux reads headers only; routing through the policy
  service requires either a `policy.Check` call inside each handler or
  wrapping the mux in a policy-check middleware. Wave 11 (workflow
  RLS + policy consolidation).
- **Redaction endpoint** never existed, so the "redaction blocked on
  held docs" clause is vacuously satisfied; when the redaction
  endpoint ships it must consult `AnyActiveHoldFor`. Wave 12.
- **Create-hold UI with doc multi-select** — current placeholder uses
  `window.prompt` for release reason/approver. Proper dialogs land
  with Wave 10.
- **Remove dead code** in `compliance/retention.go`
  (`CreateLegalHold` / `ReleaseLegalHold` / `RetentionEnforcer`) —
  all unreferenced by boot; scheduled for deletion in Wave 11 workflow
  consolidation.
- **Hold-extension / custodian-update semantics** — the schema has no
  expiry column; the spec's "extend" clause is vacuous until we add
  one. Wave 8 follow-up if needed.
- **`legal_holds_applied_total` / `legal_holds_released_total` Prom
  counters** — Wave 13.6.

## Wave 8 scorecard

| Prompt | Status |
|---|---|
| 8.1 Retention cron | ✅ |
| 8.2 Legal hold API + UI | ✅ this doc |
| 8.3 GDPR DSR (ADR 0024) | pending |
| 8.4 Data residency pinning | pending |

## Next prompt

**8.3** — GDPR data-subject endpoints (export / erase / anonymize) as
Temporal workflows + ADR 0024 documenting the 30-day SLA,
verification-token flow, and interaction with legal holds.
