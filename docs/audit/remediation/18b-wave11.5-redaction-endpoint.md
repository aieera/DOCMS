# Remediation 18b — Wave 11.5: Redaction endpoint + hold gate

**Date:** 2026-04-18
**Wave:** 11.5 · closes Wave 8.2 deferral ("Redaction endpoint + hold gate").

## Recon

Wave 8.2 DoD mentioned "Deletion, disposition, and redaction are
blocked while any hold is active." Delete was gated (Wave 8.2);
disposition was gated through retention (Wave 8.1). Redaction had
no endpoint — the invariant was vacuous.

Intelligence service (Python) already exposes
`POST /intelligence/redact/detect` + `POST /intelligence/redact/apply`
wired to a Celery task that calls PyMuPDF's `page.apply_redactions()`.
Neither endpoint consulted legal holds.

## What shipped

### Migration 000008

[services/document/migrations/000008_document_redactions.up.sql](../../../services/document/migrations/000008_document_redactions.up.sql)
— new `document_redactions` table:

- PK `(tenant_id, id)`, RLS-wrapped.
- `status ∈ {queued, applied, failed}` — orchestration ledger.
- `regions JSONB` (bbox array) + `entity_types TEXT[]` (NER-detected).
- FK to documents and users with tenant-scoped composite keys.
- Index on `(tenant_id, document_id, created_at DESC)` for the
  per-document audit view.

Append-only by design; redactions are irreversible.

### Document-service endpoint

[services/document/internal/handler/redaction_handler.go](../../../services/document/internal/handler/redaction_handler.go)
mounts `POST /api/v1/documents/{id}/redact`:

1. `callers()` → tenant + user (401 on missing).
2. `requireRole("compliance_officer", "admin", "owner")` (403 otherwise).
3. Parse `{id}` and `version_id` (400 on malformed — **before** any
   DB I/O).
4. Validate `reason` required + at least one of `regions[]` /
   `entity_types[]`.
5. **Hold gate**: `holds.AnyActiveHoldFor(tenant, doc)` → if held,
   return `ErrLegalHold` → 423 Locked.
6. In a single `WithTenantTx`: insert `document_redactions` row
   (status=queued) + outbox event `dms.document.redacted.v1`.
   Both land atomically.
7. Respond `202 Accepted` with `redaction_id`, `document_id`,
   `created_at`, note that pixel-level apply is async.

Go 1.22 method-pattern routing: registered as
`POST /api/v1/documents/{id}/redact` on rootMux. Other document
routes continue flowing to grpc-gateway's longest-prefix-match.

### Wiring

[services/document/cmd/server/main.go](../../../services/document/cmd/server/main.go)
constructs the handler with the pool + pre-existing `holdsService`
and mounts it next to the retention-policy admin mux.

### Tests

[services/document/internal/handler/redaction_handler_test.go](../../../services/document/internal/handler/redaction_handler_test.go)
— 6 HTTP validation tests, all green:

1. Missing headers → 401
2. Role = `member` → 403
3. Bad document UUID → 400
4. Missing `reason` → 400
5. Empty `regions[]` + empty `entity_types[]` → 400
6. Bad `version_id` → 400 (before DB)

```
$ go test ./services/document/internal/handler/...
ok  github.com/vaultdms/vaultdms/services/document/internal/handler  0.442s
```

## DoD — spec §7.2 redaction invariant

| Requirement | Status |
|---|---|
| Endpoint exists | ✅ `POST /api/v1/documents/{id}/redact` |
| Hold gate returns 423 Locked on held doc | ✅ via `ErrLegalHold` |
| Audit record persisted | ✅ `document_redactions` table |
| Domain event emitted | ✅ `dms.document.redacted.v1` via outbox |
| compliance_officer / admin / owner only | ✅ `requireRole` |
| Atomic audit + event | ✅ single `WithTenantTx` |

## Deferred (logged in out-of-scope.md)

- **Fan-out to intelligence service** — Celery task consumer for
  `dms.document.redacted.v1` that invokes PyMuPDF
  `apply_redactions` and flips the row to `applied`/`failed`. A
  small connector-service subscriber or a direct intelligence HTTP
  call from document-service on 202. Wave 12.
- **Frontend redaction UI** — bbox selector on the PDF viewer,
  "auto-detect PII" button calling intelligence `/redact/detect`,
  per-document redaction history panel. Wave 10 follow-up.
- **Redaction diff viewer** — before/after with the pixel regions
  highlighted for audit. Wave 12.

## Wave 11 scorecard

| Item | Status |
|---|---|
| 11.1 Workflow RLS audit | ✅ |
| 11.2 OPA gate on holds | ✅ |
| 11.3 Dead-code cleanup | ✅ |
| 11.4 DSR verification token | ✅ |
| **11.5 Redaction endpoint + hold gate** | ✅ this doc |
| 11.6 Cross-service DSR erase | pending |
| 11.7 Per-region KEK aliases | pending |

## Next prompt

**11.6 — cross-service DSR erase activities.** Today
`OverwriteSubjectPII` in the workflow service only touches
`users` + `audit_events` (document-service tables). Subjects with
data in qdrant (vector store), search (opensearch), notification
(notifications table), and connector (oauth tokens) still have
their data linger after an erase workflow completes. Each service
registers its own Temporal activity; the erase workflow dispatches
all of them in a fan-out with per-activity retry.
