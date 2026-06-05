# 02 — Google Drive import + reusable server-side ingest pipeline

**Commit:** `3f7f5a7` · **Type:** feature (ADR 0089) · **Date:** 2026-06-05

## Summary

Built the Google Drive import action on top of the existing OAuth slice, plus a
reusable server-side ingest pipeline. `POST /api/v1/connectors/google/drive/import`
lists a Drive folder, downloads each file (Google-native Docs/Sheets/Slides
exported to PDF; folders + Forms/Sites skipped), and ingests each as a real
document **with a version** — so imports OCR + embed + become searchable like a
browser upload.

## Two cluster-only problems solved

1. **Auth.** `POST /api/v1/documents` is gated by `SessionOrAPIKey` — a gateway
   signature is not an identity. The import runs **as the caller**: their
   `dms_session` token is forwarded as a cookie on the document REST calls.
   Storage upload is driven over **gRPC directly** (metadata-authorized),
   bypassing the REST proxy's `SessionOrAPIKey` gate.
2. **Presigned host.** Storage signs PUT URLs against `SEDOC_S3_PUBLIC_BASE`
   (`localhost:9000`) for the browser; an in-cluster caller can't reach that and
   SigV4 signs the Host. A custom dialer connects to the internal MinIO endpoint
   (`minio:9000`) while keeping the signed Host — MinIO validates the signature
   against the header, not the socket.

## What landed

- Provider `DownloadDriveFile` (binary `alt=media` + Google-native export).
- Reusable `services/connector/internal/ingest/ingest.go` (the 5-step
  create-doc → initiate → PUT → complete → version flow).
- `service/drive_import.go` `ImportDriveFolder` (list → download → ingest,
  per-file result summary).
- Handler + route + `main.go` wiring.
- Frontend `DriveImportPanel` in the Google connector modal (workspace + folder
  pickers, per-file outcome).

## Verified

End-to-end via a temporary smoke endpoint (since removed): synthetic PDF →
createDoc → storage gRPC → presigned PUT via the dialer → CompleteUpload →
version → OCR → embed → found in hybrid search in ~15s. The route is reachable
through both the Vite dev proxy and Kong (prod).

**Not live-tested:** the Drive-fetch half needs a connected Google account with
files (none in the dev env); the code is complete and unit-reasoned.

## Files changed
`services/connector/cmd/server/main.go`,
`services/connector/internal/handler/{google,handler}.go`,
`services/connector/internal/ingest/ingest.go` (new),
`services/connector/internal/providers/google/google.go`,
`services/connector/internal/service/{drive_import.go (new),service.go}`,
`web/src/api/connectors.ts`,
`web/src/components/admin/{DriveImportPanel.tsx (new),GoogleWorkspaceModal.tsx}`.
