# ADR 0088 — Server-side watched-folder intake

Date: 2026-05-13
Status: Accepted (the blueprint reserved ADR 0079 for this prompt
but 0079 was taken by Redaction Review months ago. Using 0088 —
same shift as 0078↔0087 noted in ADR 0087.)

## Context

§12.4-style scanner ingestion requires two surfaces:

1. **Server-side drop folder** — a daemon watching a directory
   (local FS, NFS, SMB share, S3 prefix). New file appears → it
   becomes a new document in a tenant-configured folder → OCR +
   classify pipelines fire automatically. The blueprint's stated
   use case is "network scanners drop PDFs here every night."

2. **Desktop scanner client** — Windows/macOS app with TWAIN/WIA
   bindings. Talks to the local scanner driver, uploads through
   the existing REST API. Separate scope.

This ADR covers (1). The desktop client is a downstream follow-up
that doesn't need any backend changes (it's just another REST
client).

## Decision

### Data model

Two new tables, both per-tenant + RLS-isolated:

`intake_drop_folders`
```
tenant_id, id, label, active,
host_path,                 -- absolute path the watcher tails
target_workspace_id,
target_folder_id,
quarantine_subdir,         -- relative; "quarantine" if NULL
processed_subdir,          -- relative; "processed" if NULL
recurse,                   -- whether to recurse into subdirs
extensions_csv,            -- "pdf,tiff,jpg,png" — empty = any
created_by, created_at, updated_at,
last_seen_at, files_ingested, last_error
```

`intake_ingested_files`
```
tenant_id, id, folder_id,
source_path,               -- original path, relative to host_path
sha256,                    -- content hash; the dedup key
size_bytes,
document_id,               -- NULL if create failed
ingest_status,             -- pending | ingested | failed | quarantined
ingest_error,
ingested_at
```

`UNIQUE (tenant_id, folder_id, sha256)` is the idempotency key —
the same bytes dropped twice land in `intake_ingested_files` once
with a `document_id` reference both can find.

### Watcher implementation

A goroutine inside the connector service:

1. On boot + every 60s, loads all active drop-folder configs.
2. For each config, opens (or reuses) an `fsnotify` watcher on
   `host_path`. Optional recursion adds nested directories.
3. On `WRITE` / `CREATE` events:
   - Wait 2s for the file to "settle" (handles scanners that write
     in chunks).
   - Compute SHA-256 + MIME-detect.
   - `INSERT ... ON CONFLICT DO NOTHING` into `intake_ingested_files`.
     If the insert returns 0 rows affected, the file is a duplicate
     — move to `processed_subdir` and continue.
   - Call `DocumentClient.MaterialiseFile()` (the same client the
     email worker uses for envelope materialisation). Receives the
     new document_id.
   - On success: move source file into `processed_subdir/` and stamp
     `ingest_status='ingested'`, `document_id=<id>`.
   - On failure: move source file into `quarantine_subdir/` and
     stamp `ingest_status='failed'`, `ingest_error=<msg>`. Operator
     reviews via the admin UI.
4. On scanner mid-write or partial files, the 2s settle window
   prevents racing with the writer. A file whose size grows during
   the wait is re-queued for another 2s.

### Per-tenant directory layout

`/data/intake/{tenant-slug}/inbox/` is the convention. Recursing
subdirs (config flag) allows scanners that file by department:
`inbox/finance/`, `inbox/hr/`. Each tenant config picks its own
`host_path` so the multi-tenant deployment can mount different SMB
shares at different paths.

### OCR + classify pipeline

Already wired. The intake worker creates the document via the same
REST path the email worker uses (`POST /api/v1/documents` +
`POST /api/v1/documents/{id}/versions` with a content blob). The
storage service emits `dms.version.uploaded.v1` on every new
version, and the OCR + classify pipelines have been subscribing to
that event since Wave 6. The intake worker doesn't need to know
about them — it just creates documents like any other client.

(The same caveat from §12.5b applies: version-blob upload requires
the connector to first put bytes through storage's presigned-PUT
flow. Wave 12.5b shipped the doc-row path without the version
upload step; this ADR includes the storage-blob step so the
acceptance criterion "OCR pipeline triggered" actually green.)

### REST surface

```
GET    /api/v1/admin/intake/folders          — list configs (no secrets)
POST   /api/v1/admin/intake/folders          — create
PATCH  /api/v1/admin/intake/folders/{id}     — edit
DELETE /api/v1/admin/intake/folders/{id}     — disable
POST   /api/v1/admin/intake/folders/{id}/scan-now — manual sweep
GET    /api/v1/admin/intake/folders/{id}/recent-files — last 50 files + status
```

### Admin UI

`/admin/integrations/drop-folder` — per-tenant folder CRUD, target
workspace+folder picker, status pane showing files ingested / failed
/ pending. A "Test" button writes a placeholder file into the
configured directory and watches for ingestion to confirm the path
is reachable from the connector container.

## Consequences

- The connector container must have read+write access to whatever
  directory each tenant configures. Docker compose mounts
  `/data/intake/` as a named volume; Helm chart users mount their
  own SMB / NFS PVCs.
- One fsnotify watcher per config = one OS-level handle. For
  large tenants with many configs this could hit the inotify
  watcher limit on Linux (default 8192); the worker logs a
  warning and falls back to 30s polling on watcher-exhausted
  configs. A future wave can introduce a single watcher with a
  prefix dispatch.
- Quarantined files stay on disk until an admin manually removes
  them — no auto-cleanup. The admin UI shows the count + an
  "Open quarantine" link to inspect.

## Not chosen

- **S3-prefix watcher** (using SQS notifications). Out of scope for
  v1 — local FS / SMB / NFS covers the blueprint use case. SQS
  bucket-notification mode lands when a customer asks.
- **Per-file metadata extraction** from filename pattern. The intake
  worker creates documents with `title = filename`. Regex-based
  metadata extraction (e.g. `Invoice_2026-04-19_$2500.pdf` →
  `{date: 2026-04-19, amount: 2500}`) is a routing-rules-style
  follow-up similar to §12.5c's deferred rule mapping for email.
- **Bidirectional sync** (deleting a document deletes the source
  file). Source files are moved to `processed/` and stay there as
  the audit trail; document deletion is independent.
