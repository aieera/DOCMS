# SeDoc ERP Integration Guide

This is the contract the ERP outbound-sync integration is built against: how to
authenticate, upload bytes, create-or-version documents by your own business key,
drive the pre-commit ingestion pipeline, work the review queue, and rely on
idempotency so retries never duplicate.

The machine-readable spec for every route here lives in
[`proto/gen/openapi/sedoc.swagger.json`](proto/gen/openapi/sedoc.swagger.json)
(tags: *External Key (Upsert)*, *Ingestion*, *Review Queue*, *Storage Upload*,
*Bulk*, *Download*). That file is the generated gateway spec merged with the
hand-wired routes (`proto/openapi/handwired.swagger.json` via
`scripts/merge-openapi.py`, run by `make proto-gen`).

All paths are under `/api/v1`. JSON unless noted.

---

## 1. Authentication

Service-to-service callers send a Bearer **API key**:

```
Authorization: Bearer vdms_<key>
```

Keys carry **scopes**; each route requires one:

| Scope | Used by |
|---|---|
| `documents:read` | byExternalKey, list ingestion items, list/get review queue, downloads |
| `documents:write` | upsert, ingest, review resolve |
| `documents:delete` | `DELETE /documents/{document_id}` — remove a document the ERP created |
| `upload` | storage upload proxy (initiate / complete / abort) |

The browser uses the `dms_session` cookie instead; the same routes accept either.
A missing/insufficient scope → `401`/`403` with `{type, message, correlation_id}`.

---

## 2. Idempotency (read this before any write)

Every resource-creating POST is replay-safe via the `Idempotency-Key` header
(tenant-scoped, ≤200 chars). Use a **stable** key per logical record — e.g. your
`dms_sync_log` row id, NOT a random per-attempt uuid.

- First call with key `K` → runs, stores the response.
- Any later call with `K` → replays the stored response verbatim, header
  `Idempotency-Replayed: true`, and **creates nothing new**.
- The key is **required** for API-key callers on creating POSTs (`:upsert`,
  `/ingest`, storage `initiate`/`complete`). Omitting it → `400
  IDEMPOTENCY_KEY_REQUIRED`. (Session/browser callers are exempt.)

Status semantics:

| Code | `type` | Meaning |
|---|---|---|
| 400 | `IDEMPOTENCY_KEY_REQUIRED` | Creating POST from an API key without the header |
| 400 | `IDEMPOTENCY_KEY_TOO_LONG` | Key > 200 chars |
| 409 | `IDEMPOTENCY_IN_PROGRESS` | A request with this key is still running — retry shortly |
| 422 | `IDEMPOTENCY_KEY_REUSED` | Key already used for a *different* method+path |
| 503 | `IDEMPOTENCY_STORE_UNAVAILABLE` | Transient store error — retry |

There are **two layers** of dedupe and they compose:
1. `Idempotency-Key` — replays the exact HTTP response on retry.
2. `external_id` — even with a fresh key, upsert/bulk converge on the same
   document because the business key is tenant-unique.

**Rate limiting.** `:upsert` and `/ingest` are per-tenant rate limited
(default 600/min, override via ops). Over the limit → `429` with `Retry-After`.

---

## 3. Uploading bytes (prerequisite for upsert + ingest)

Documents/versions/ingestion all reference a blob that's **already in storage**.
Three steps:

1. **Initiate** — `POST /storage/uploads/initiate`
   ```json
   { "filename": "INV-2024-00188.pdf", "mime_type": "application/pdf",
     "size_bytes": 51234, "sha256_hash": "<hex>", "workspace_id": "...", "folder_id": "..." }
   ```
   → `{ "upload_id", "presigned_put_url", "storage_bucket", "storage_key", "expires_at" }`.
   If you send `sha256_hash` and the blob already exists, the response is
   deduplicated (skip step 2).

2. **PUT the bytes** directly to `presigned_put_url` (not through SeDoc).

3. **Complete** — `POST /storage/uploads/{upload_id}/complete`
   ```json
   { "sha256_hash": "<hex>", "size_bytes": 51234 }
   ```
   → `{ "content_blob_id", "checksum_sha256", "storage_bucket", "storage_key", "size_bytes" }`.
   Verifies the checksum, virus-scans, envelope-encrypts, persists the blob.

Use the returned `content_blob_id` (or the `checksum_sha256`) as the blob
reference in the next step.

---

## 4. Create-or-version by your business key (`:upsert`)

`POST /api/v1/workspaces/{workspace_id}/documents:upsert`

```json
{
  "external_id": "INV-2024-00188",
  "folder_id": "1f0c…",
  "title": "Invoice 00188",
  "document_class": "invoice",
  "version": { "blob_checksum": "<sha256 hex>", "blob_ref": "<content_blob_id>" }
}
```

Semantics (idempotent on `(tenant, external_id)`):

| State | Result | HTTP |
|---|---|---|
| No doc for `external_id` | Create document + version 1 | `201` |
| Exists, **same** checksum | No-op, returns current version | `200`, `version_created:false` |
| Exists, **new** checksum | Append the next version, repoint head | `200`, `version_created:true` |

Response:
```json
{ "document_id", "current_version_id", "current_version_number", "created", "version_created" }
```

Resolve later with `GET /api/v1/documents:byExternalKey?external_id=INV-2024-00188`
→ `{ "document_id", "current_version_id" }` (404 if unknown *or* not viewable —
existence isn't leaked).

Use `:upsert` when **you** decide the document identity. Use `/ingest` (next)
when you want the system to read the file and decide.

**Removing a document you created** — `DELETE /api/v1/documents/{document_id}`
(scope `documents:delete`) → `200` with an empty `{}` body. This is a soft delete: the document moves
to the tenant's Trash (an admin can restore it), the search index drops it, and
`dms.document.deleted.v1` is emitted. Repeating the call → `404` (already
gone). Refused with `423 LEGAL_HOLD` while the document is under legal hold,
and with `409` when it is a declared record or inside WORM retention — those
are disposed through their own flows, never through delete.

---

## 5. Pre-commit ingestion (`/ingest`) — OCR-before-routing

For inbound files where you DON'T yet know if it's a new document or a new
version of an existing one. The blob is staged, OCR'd, a business key is
extracted, then a workflow routes it.

`POST /api/v1/ingest`
```json
{ "workspace_id": "...", "folder_id": "...", "target_customer_ref": "CUST-42",
  "blob_checksum": "<hex>", "content_blob_id": "<id>", "document_class": "invoice" }
```
→ `201 { "ingestion_item_id", "status": "received" }`. No document/version is
created yet. Idempotent on `(tenant, blob_checksum, target_customer_ref)`.

Lifecycle (poll `GET /api/v1/ingest/items?status=…` or consume the events):

```
received → ocr_running → processed → routed (committed)         ← high confidence
                                   ↘ needs_review               ← low confidence / ambiguous
```

- **≥ 0.85 confidence + matches an existing external key** → auto-committed as a
  **new version** of that document. No orphan staging doc.
- **≥ 0.85 + no match** → auto-committed as a **new document** (v1).
- **< 0.85 / ambiguous / no key** → a **review queue** item; nothing is versioned.

Events (NATS, JSON CloudEvents): `dms.ingestion.received.v1` →
`dms.ingestion.processed.v1` → `dms.ingestion.routed.v1` *or*
`dms.review.created.v1`.

---

## 6. Review queue (human triage)

Low-confidence ingestion reads land here. Keyset-paginated.

- `GET /api/v1/review-queue?status=pending&limit=50&cursor=<opaque>`
  → `{ "items": [ {id, ingestion_item_id, extracted_external_key,
  suggested_match_document_id, confidence, reason, status, …} ], "next_cursor" }`.
  Page until `next_cursor` is empty.
- `GET /api/v1/review-queue/{id}` → one item **plus its OCR text** for the reviewer.
- `POST /api/v1/review-queue/{id}/resolve`
  ```json
  { "decision": "new_version", "target_document_id": "…" }   // or
  { "decision": "new_document", "external_key": "INV-…" }     // or
  { "decision": "reject", "notes": "not an invoice" }
  ```
  Commits via the same upsert/version path, clears the ingestion item, emits
  `dms.review.resolved.v1`. Re-resolving an already-resolved item → `409`.

---

## 7. Bulk backfill

For the initial load. `POST /api/v1/admin/bulk/import` with an **NDJSON** body:
an optional first envelope line, then one resource per line.

```
{"request_id":"backfill-2026-06-15","batch_size":200}
{"resource":"workspace","workspace":{"external_id":"W-1","name":"Finance"}}
{"resource":"document","document":{"external_id":"INV-1","title":"…","workspace_external_id":"W-1","content_blob_id":"…"}}
```

Response is an NDJSON stream of per-item results. Documents are processed through
a bounded worker pool and are **idempotent on `external_id`**: re-running the same
data (even with a new `request_id`) creates nothing new, and duplicate
`external_id`s within one batch converge to a single document. Re-sending the
exact same `request_id` replays the cached batch response.

---

## 8. Downloads

- `GET /api/v1/documents/{id}/content` — current version bytes.
- `GET /api/v1/documents/{document_id}/versions/{version_id}/download` — a
  specific version (redirects to a presigned URL for plaintext blobs).
- `GET /api/v1/documents/{document_id}/versions/{version_id}/decrypt-stream` —
  decrypted bytes for envelope-encrypted blobs.

---

## 9. Errors

All errors share `{ "type", "message", "correlation_id" }`. Common `type`s:
`VALIDATION`, `UNAUTHORIZED`, `FORBIDDEN`, `NOT_FOUND`, `ALREADY_EXISTS`,
`CONFLICT`, plus the `IDEMPOTENCY_*` codes in §2. Log `correlation_id` — it ties
your request to the server-side trace.
