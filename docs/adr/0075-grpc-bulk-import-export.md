# ADR 0075 — gRPC Bulk Import + Export

Date: 2026-05-10
Status: Accepted (mTLS deferred to a separate ADR)
Closes: blueprint §12.1 — "gRPC for service-to-service + bulk import/export"

## Context

Tenant onboarding and migration off legacy DMSes are the primary use
cases. Today the only way to ingest a tenant's existing corpus is to
loop through `POST /api/v1/documents` for each row, and each document
goes through the per-call validation + outbox emission + permission
check + grpc-gateway round-trip. A 50k-document migration takes
hours; a 1M-document migration is impractical.

The blueprint §12.1 specifies gRPC streaming for bulk:

> gRPC | Service-to-service internal communication, **high-throughput
> integrations (bulk import/export)** | Protobuf over HTTP/2

The other side (high-throughput service-to-service) is already in
place — every Go service binds gRPC on its own port, the document
service calls policy/storage over gRPC, the signature service calls
document over gRPC, etc. What's missing is the **bulk** surface:
streaming RPCs that collapse N round-trips into one bidirectional
stream, with idempotency so retries don't dup, and external-id
mapping so cross-references inside a single import resolve.

mTLS for the internal gRPC plane is **deferred** to its own ADR.
It's real ops work (cert authority, distribution, rotation, fail-
closed semantics) and shouldn't ride this PR.

## Decision

### One BulkService, all 5 entity types

`proto/vaultdms/v1/bulk.proto` defines a single `BulkService` with
two RPCs:

```proto
service BulkService {
  rpc BulkImport (stream BulkImportRequest)  returns (stream BulkImportResponse);
  rpc BulkExport (BulkExportRequest)         returns (stream BulkExportResponse);
}
```

Resource discrimination is via `oneof` inside the request, not via
separate per-resource RPCs. Reasoning:

- A real migration crosses entity types — workspaces and folders
  arrive before the documents that live inside them. One stream
  per request lets the import framework process them in order with
  cross-reference resolution (a folder can reference a workspace
  by external_id; a document can reference a folder).
- Per-resource RPCs would either force the client to open N
  parallel streams (correctness nightmare around ordering) or add
  a separate orchestrator on top.

Five resource variants in v1: `BulkWorkspace`, `BulkFolder`,
`BulkDocument`, `BulkUser`, `BulkGroup`. Each carries the subset of
the corresponding Create RPC that's safe to bulk-set.

### External-id mapping, not internal UUIDs

Every bulk item carries an `external_id` (the tenant's natural key
in their previous system) instead of an internal UUID. The bulk
service:

1. Looks up `(tenant_id, resource_type, external_id)` in the
   `bulk_external_id_map` table.
2. Hit → maps to existing internal UUID; this is an idempotent
   upsert / no-op.
3. Miss → mints a new internal UUID, creates the entity, records
   the mapping in the same transaction.

Cross-references inside the same import use external ids:

```
BulkFolder { external_id: "F-1", workspace_external_id: "W-1", ... }
BulkDocument { external_id: "D-1", folder_external_id: "F-1", ... }
```

The processor resolves `workspace_external_id` → internal UUID via
the map at write time. Order matters within a stream — workspaces
before folders before documents — and the processor returns an
error if a referenced external id hasn't been seen yet.

### Idempotency

Two layers:

1. **External-id map**: re-importing the same row is a no-op.
   Same external_id → same internal UUID → upsert `(name, tags,
   description, …)`.
2. **bulk_import_log**: every BulkImportRequest carries a
   `request_id` (UUID v7 minted by the client). The first ack
   stores `(tenant_id, request_id) → status`; replays return the
   cached status without re-running the processor. Retries on
   network failure resume cleanly.

### Backpressure

Bidirectional stream with explicit ack windows:

- Client sends a batch (default 100 items per `BulkImportRequest`).
- Server processes, responds with a `BulkImportResponse` containing
  per-item `Result` (success / failure with reason).
- Client waits for the response before sending the next batch.

This is plain TCP backpressure shaped through gRPC's stream — no
custom credit-based protocol. The server can't fall behind because
the client throttles itself on the response. The 100-item batch
size is configurable via a request envelope option; we cap at 1000
to bound server memory.

### Bulk export

Server-streaming RPC. Client sends a single `BulkExportRequest`
with filters (resource type, time range, optional workspace_id);
server streams matching records as `BulkExportResponse` envelopes
(one per page, default 500 records). No reverse path: this is a
read.

NDJSON-friendly: each `BulkExportResponse` carries one envelope
holding the next batch. The HTTP facade flattens this to one
NDJSON line per record.

### HTTP facade for the admin wizard

The frontend doesn't speak gRPC. The document service exposes:

- `POST /api/v1/admin/bulk/import` — accepts NDJSON body, parses
  one record per line, opens an internal gRPC stream to itself,
  writes batches, streams an NDJSON response with per-line status.
- `GET  /api/v1/admin/bulk/export?resource=document&from=&to=` —
  opens a gRPC export stream, flattens to NDJSON.

The facade is purely a transport adapter — all validation,
idempotency, and external-id resolution happen in the gRPC layer.

### Living in the document service, not a new service

Bulk lives inside the document service for v1 because:

- Document, workspace, folder are document-service-owned.
- Users + groups live in auth's DB but the auth service exposes
  CreateUser / CreateGroup gRPC; the bulk service calls those
  outbound for non-document resources.
- One less deployment artifact. If bulk grows its own concerns
  (rate limiting, dedicated outbox, scheduling) it can split out
  later.

The bulk_import_log + bulk_external_id_map tables live in the
document service's schema (migration 000038). Auth service rows
written via the bulk path get their external-id mapping recorded
in document's table — auth doesn't need to know about bulk.

### Failure semantics

Per-item, not per-batch. If item 3 of a 100-item batch fails:

- The transaction for item 3 rolls back.
- Items 1, 2, 4..100 commit.
- The response carries `Result { success: false, error: "..." }`
  for item 3 and `success: true` for the rest.
- The client decides whether to retry the failed items.

This matches the expectations of every bulk-API client we'd
integrate with (DocuSign, Salesforce, Slack). The alternative
(all-or-nothing per batch) is correct but unhelpfully strict for
a migration where 1 of 100 docs has a bad mime type.

### Out of scope (deferred)

- **Dry-run mode** — would require a separate validation pass.
  The current path is "import; failed rows reported, commit
  successful ones". Add later if requested.
- **CSV parsing** — clients convert to NDJSON. Avoids us shipping
  a CSV parser + delimiter / quoting / encoding semantics.
- **Live progress streaming on the frontend** — facade returns
  one final NDJSON status per line; the wizard renders the
  summary on completion. Real-time progress is a follow-up that
  needs SSE or websockets on the facade.
- **mTLS for the internal gRPC plane** — separate ADR.
