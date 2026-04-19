# Remediation 11b — Wave 2 (part 1): share-link UI + restore-version

**Date:** 2026-04-17
**Source finding:** [11-coverage-matrix.md](../11-coverage-matrix.md) Wave 2
— two of the three items are in this doc. The third (storage gateway)
is intentionally deferred pending an architectural call; see the end.

## Items landed

### B2-12 — Share-link dialog (unblocks smoke test step 11-12)

Backend already served `POST /documents/{id}/share-links`,
`GET /documents/{id}/share-links`, `DELETE /share-links/{id}`, and
`POST /shared/{token}` via grpc-gateway on the document service
([proto/vaultdms/v1/document.proto:264-274](../../../proto/vaultdms/v1/document.proto#L264)).
The service-layer implementation in
[services/document/internal/service/sharing_tags.go:33](../../../services/document/internal/service/sharing_tags.go#L33)
already generates a 32-byte crypto token, SHA-256-hashes it for storage,
and returns plaintext once. So the only work was client-side.

**Files:**

- [web/src/api/shareLinks.ts](../../../web/src/api/shareLinks.ts) — new
  adapter file with `createShareLink`, `listShareLinks`,
  `deleteShareLink`, `accessShareLink`.
- [web/src/components/documents/ShareDialog.tsx](../../../web/src/components/documents/ShareDialog.tsx)
  — rewrote to call `createShareLink()` instead of minting a
  client-side UUID. Password/expiry/permission controls now forward to
  the backend. Adds `data-testid="share-link-result"` so the smoke test
  can assert the generated URL appears.

### C-3 — `RestoreVersion` RPC

Proto + service + handler additions. **Note:** requires
`cd proto && buf generate` before the Go code will compile — the
generated `pb.go` / `pb.gw.go` files under `proto/gen/go/...` don't yet
contain the new `RestoreVersionRequest` message or the
`RestoreVersion` client/server stubs.

**Files:**

- [proto/vaultdms/v1/document.proto](../../../proto/vaultdms/v1/document.proto)
  — added `message RestoreVersionRequest { document_id, version_id, note }`
  and `rpc RestoreVersion(...) returns (Version)` with the HTTP mapping
  `POST /api/v1/documents/{document_id}/versions/{version_id}/restore`.
- [services/document/internal/service/documents.go](../../../services/document/internal/service/documents.go)
  — `DocumentService.RestoreVersion(ctx, documentID, versionID, note)`:
  verifies edit permission, refuses on legal hold, copies the source
  version's `content_blob_id` + `sha256_hash` + `size_bytes` forward as
  a new `NextVersionNumber()`, sets it as `current_version_id`, and
  emits `dms.version.restored.v1` (new event type — reuses
  `VersionCreatedPayload`). The original version row is preserved;
  restore is non-destructive.
- [services/document/internal/handler/handler.go](../../../services/document/internal/handler/handler.go)
  — new gRPC handler method `RestoreVersion` parses the two UUIDs and
  delegates.

The frontend adapter `restoreVersion()` in
[web/src/api/documents.ts](../../../web/src/api/documents.ts) was
already present from an earlier pass — it now hits a real endpoint.

## Known IDE diagnostics until `buf generate` runs

- `services/document/internal/handler/handler.go` references
  `vaultdmsv1.RestoreVersionRequest`.
- The proto lint job in CI runs `buf generate` + `git diff --exit-code
  proto/gen/` which will regenerate the stubs cleanly.

Local reproduction:

```bash
cd proto && buf generate
```

## Deferred: storage gateway (C-4, C-5)

Storage service is gRPC-only today. `storage.proto` has no
`google.api.http` annotations on any of its 7 RPCs (InitiateUpload,
CompleteUpload, AbortUpload, GetDownloadURL, GetPreviewURL,
GetScanStatus, RequestLifecycle). The service's `main.go` has no HTTP
mux beyond healthz.

Two candidate approaches, both non-trivial:

**(a) Add grpc-gateway to storage service directly.** Mirrors the
document service pattern. Requires:
- `google.api.http` annotations on storage.proto RPCs.
- New HTTP port in the storage config + middleware chain
  (`SessionAuth → tenant → gateway`).
- Session-cookie handling in storage — today storage only trusts an
  internal API key from the document service, never sees a user
  session. Adding session validation couples storage to the auth
  service's session-cache protocol.

**(b) Proxy uploads through the document service.** Document service
already has session auth, tenant resolution, and a gRPC client to
storage. Add REST handlers under
`/api/v1/storage/uploads/...` in the document service that forward to
the storage gRPC client.
- Keeps storage as an internal service with the trust boundary intact.
- Duplicates the handler surface, but the handlers are thin pass-throughs.
- Consistent with how **frontend already calls** `/api/v1/storage/...`
  without thinking about which service owns it.

Recommendation: **(b)**. Please confirm before I start — this is a
trust-boundary change either way and worth a minute of your attention.

## Wave 2 scorecard

- B2-12 share-link: ✅
- C-3 restore-version: ✅ (pending `buf generate`)
- C-4/C-5 storage gateway: 🟡 deferred — design call needed
