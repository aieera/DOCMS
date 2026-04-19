# Remediation 11c — Wave 2 (part 2): storage REST proxy on the document service

**Date:** 2026-04-17
**Source finding:** [11-coverage-matrix.md](../11-coverage-matrix.md) rows
C-4 and C-5. Chosen approach: **proxy option (b)** — add REST handlers
on the document service that forward to the storage gRPC client, rather
than adding grpc-gateway + session auth to the storage service itself.
Rationale: preserves the storage trust boundary (storage continues to
trust only internal gRPC callers, never session cookies) and matches
the existing frontend paths (`/api/v1/storage/*`) with no client change.

## What shipped

### Config — new `StorageServiceAddr`

[pkg/config/config.go](../../../pkg/config/config.go)

- Added `StorageServiceAddr string` field; env
  `STORAGE_SERVICE_ADDR`; default `storage:9090`. Same pattern as
  `PolicyServiceAddr`.

### Document service wires a storage gRPC client + proxy

[services/document/cmd/server/main.go](../../../services/document/cmd/server/main.go)

- New `grpc.DialContext` to `cfg.StorageServiceAddr` with the same
  fail-open pattern as the policy client (warn + proceed; proxy handlers
  return 503 if client is nil).
- New `handler.NewStorageProxy(storageClient)` wired into the root mux
  on the `/api/v1/storage/` prefix ahead of the grpc-gateway catch-all.

### Proxy handler

[services/document/internal/handler/storage_proxy.go](../../../services/document/internal/handler/storage_proxy.go)
(new file)

Four routes, thin gRPC forwarders:

- `POST /api/v1/storage/uploads/initiate`
- `POST /api/v1/storage/uploads/{upload_id}/complete`
- `POST /api/v1/storage/uploads/{upload_id}/abort`
- `GET /api/v1/storage/downloads/{document_id}/{version_id}`

Auth propagation: the proxy copies the inbound `X-Tenant-ID` and
`X-User-ID` HTTP headers onto outbound gRPC metadata using
`pkg/middleware.TenantMetadataKey`, so the storage service's
`TenantInterceptor` sees the same tenant as the document service does.
This mirrors exactly how the existing grpc-gateway flows into
`/api/v1/documents/*` today.

Body shape tolerance: the frontend [api/upload.ts](../../../web/src/api/upload.ts)
has historically used `sha256_hash` while the proto field is
`checksum_sha256`. The proxy accepts **both** names for back-compat, and
ignores `workspace_id`/`folder_id` on initiate (those belong to document
creation, not the storage upload session). Complete tolerates an empty
body (frontend posts `{sha256_hash: undefined}` which is effectively
empty).

Error mapping: gRPC status codes are mapped to HTTP status codes in
`grpcToHTTP()`. A nil storage client (startup failure) surfaces as 503
with a clear `"storage service unreachable"` message.

## Frontend impact

Zero client changes — the existing [api/upload.ts](../../../web/src/api/upload.ts)
and [hooks/useUpload.ts](../../../web/src/hooks/useUpload.ts) call the
exact paths the proxy now serves.

## What's explicitly NOT in scope here

- **Dedup response** (`deduplicated`, `existing_blob_id` in
  [types/api.ts:115](../../../web/src/types/api.ts#L115)). The
  storage.proto's `InitiateUploadResponse` doesn't expose these fields;
  the platform either never implemented dedup, or it lives inside a
  different request path the proxy isn't aware of. Leaving the frontend
  fields as `?:` optional as they are today — they'll always come back
  undefined.
- **Preview URL and scan-status RPCs** exist on storage.proto
  (`GetPreviewURL`, `GetScanStatus`) but no frontend consumer yet.
  Proxy skipped them to keep this change focused.
- **Signed-URL sanitization through `S3PublicBase`** — the storage
  service already handles this before returning; the proxy forwards the
  URL verbatim.

## Verification

Manual smoke path (once services are up):

```bash
# Login to get the session cookie + tenant header plumbing.
# Then:
curl -sS -b cookies.txt -H "X-Tenant-ID: $TID" -H "X-User-ID: $UID" \
  -H "Content-Type: application/json" \
  -X POST http://localhost:8084/api/v1/storage/uploads/initiate \
  -d '{"filename":"t.pdf","mime_type":"application/pdf","size_bytes":100}'
```

Expected: 200 with `upload_id` + `presigned_put_url` + `expires_at`.

With storage down: 503 with
`{"type":"Unavailable","message":"storage service unreachable"}`.

## Wave 2 is now complete

- B2-12 share-link dialog ✅ (11b)
- C-3 restore-version ✅ (11b, pending `buf generate`)
- C-4 / C-5 storage REST ✅ (this doc)

Next up, pending your go-ahead: **Wave 3 — workspace CRUD**
(C-7/C-8 + B2-08/09/10). That's a proto addition +
`WorkspaceService` RPCs + repository + migration + frontend list/create
route. Meaningful scope; the workspace table exists but
`ListWorkspaces` / `CreateWorkspace` RPCs don't, and the current
frontend hardcodes a "Default Workspace" navigation target.
