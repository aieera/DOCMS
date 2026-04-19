# 0021 — `dms.version.uploaded.v1` is emitted from the document service, not storage

- **Status:** Accepted
- **Date:** 2026-04-17
- **Deciders:** Audit agent (Wave 5 Prompt 5.1 execution)
- **Supersedes:** —
- **Superseded by:** —

## Context

`DMS Architecture/final.md` § 4.3 (Prompt 5.1) directs the storage service
to publish `dms.version.uploaded.v1` from its `CompleteUpload` RPC, with
a payload including `version_id`.

Recon of the live codebase surfaces a contradiction:

- Storage's `CompleteUpload` writes `content_blobs` and closes the
  `upload_sessions` row. It has no knowledge of the `versions` table.
- The `versions` row is created later by the **document service's**
  `CreateVersion` RPC, which links an existing `content_blob_id` to a
  new version row and assigns the `version_id`.
- The existing upload flow is explicitly two-phase: client calls
  storage's `CompleteUpload` (gets a `content_blob_id`), then calls
  document's `CreateVersion` with that blob id.

Therefore `version_id` is **undefined** inside storage's CompleteUpload
transaction. Emitting `dms.version.uploaded.v1` from storage would
require either (a) making storage aware of versions (breaks service
boundaries) or (b) emitting without the field (violates the spec's own
payload contract).

Downstream consumers (OCR, classify, embed, preview) need the event to
carry enough information to fetch the object and tag all derived
artifacts by `version_id`. Those requirements are satisfied only once
the version row exists.

## Decision

Emit `dms.version.uploaded.v1` from the **document service's**
`CreateVersion` service method, in the same SQL transaction that
inserts the `versions` row and calls `SetCurrentVersion`. This
transaction already has access to both the version row and the content
blob metadata (via `repos.ContentBlobs` or by joining on
`content_blob_id`), so the full payload (storage_uri, sha256, size,
mime, uploaded_by, uploaded_at) can be assembled without cross-service
calls.

The existing `dms.version.created.v1` event emitted by CreateVersion is
**renamed** to `dms.version.uploaded.v1` and its payload extended to the
spec's shape. No consumer in the codebase subscribes to
`dms.version.created.v1` today (grep-confirmed), so the rename is safe.
A grep-based CI guard will reject reintroduction of the old subject.

Storage's existing emission of `dms.storage.upload_completed.v1` is
retained. It signals blob-level completion for storage-internal
observability; it is not the trigger for the intelligence pipeline.

## Consequences

**Easier**

- Payload is fully populated; no optional/empty `version_id`.
- No cross-service coordination needed for the event.
- Service boundaries preserved: storage stays oblivious to the
  versioning model.
- Restore-version (Wave 2a) already goes through CreateVersion's
  outbox path, so restored versions automatically emit the same event
  without separate plumbing.

**Harder**

- The event does not fire until `CreateVersion` is called. If a client
  completes an upload but never calls CreateVersion, OCR never runs.
  This was already the case and is the correct behaviour: orphan blobs
  should not be processed.
- Spec and code disagree textually. Mitigated by this ADR and by an
  annotation added at `DMS Architecture/final.md` § 4.3 referencing
  ADR 0021.

**Neutral**

- The DLQ / dedupe contracts Prompt 5.2 and 5.3 require are unaffected
  by the emission point.

## Alternatives considered

1. **Emit from storage with `version_id=""`** — rejected. Violates the
   spec's own payload contract and forces consumers to handle an
   incomplete message.

2. **Have storage receive `version_id` as an input from the client on
   CompleteUpload** — rejected. Reverses the flow: the client would
   need to create the version row first (via document service), then
   send its id back to storage on upload completion. Adds a round-trip
   and inverts the current contract.

3. **Two events: `dms.blob.uploaded.v1` from storage +
   `dms.version.uploaded.v1` from document** — rejected for Wave 5
   scope. Adds a stream, a consumer, and an intermediate state without
   solving the actual consumer need (every current consumer wants the
   version-scoped event). Reconsider if a future requirement wants
   blob-level observability outside storage itself.

## Sources

- `DMS Architecture/final.md` § 2.2, § 4.3 (Prompt 5.1), § 4.4 (Prompt 5.2).
- `services/storage/internal/service/service.go:466-529` (CompleteUpload tx body).
- `services/document/internal/service/documents.go:391-473` (CreateVersion tx body).
- `services/document/migrations/000001_initial_schema.up.sql:252-358` (content_blobs, versions tables).
- `docs/audit/11-backend-surface.md:351` — original identification of the missing publisher.
