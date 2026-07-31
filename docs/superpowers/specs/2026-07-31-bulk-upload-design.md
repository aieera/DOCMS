# Bulk content upload (ZIP / folder → workspace tree) — design

**Status:** approved design, pre-implementation
**Date:** 2026-07-31
**Area:** `web/` admin Bulk page + `services/document` (v2 only)

## Problem

The current `/admin/bulk` tool ([services/document/internal/bulk](../../../services/document/internal/bulk/service.go)) is an **NDJSON metadata-migration importer**: it upserts `workspace / folder / document / user / group` rows linked by `external_id`, and a `document` line attaches content **only** by referencing an already-uploaded `content_blob_id` (a UUID). It never uploads a file. It is admin/IT-oriented ("tenant migration off legacy DMSes") and unusable by a non-developer who simply wants to put a pile of files and folders into SeDoc.

## Goal

A non-developer path to **bulk-upload real content**: drop a `.zip` (or a folder), have its directory structure become the workspace/folder hierarchy, and each file become a document with content that goes through the normal pipeline (virus scan, OCR, dedup, versioning, region_pin). Keep the NDJSON tool for IT, demoted to an "Advanced / migration" tab.

## Scope & decisions (locked)

- **Scale:** phased. **v1 = client-side** (browser unzips + uploads per file via the existing API; no backend change). **v2 = server-side** worker (GB-scale, resumable) added later **without reworking the UI**, via a shared plan/result contract.
- **Target mapping:** per-import the admin chooses **New workspace** (name prefilled from the zip, editable) **OR Existing workspace + optional target folder**. The tree is created under the destination.
- **Conflict policy (default `new_version`):** same path as an existing document → append a new version. Options: `skip`, `rename`. Non-destructive default.
- **Metadata:** v1 = filename → document title, directory path → folder hierarchy. An optional `manifest.csv` (per-file tags/classification) is **v2 / out of scope**.
- **NDJSON:** unchanged; moved to a second "Advanced / migration" tab.

## UX — the wizard (new default tab "Upload files & folders")

1. **Source** — drop a `.zip` OR drag a folder (`webkitdirectory`). Both produce a `path → File` map.
2. **Preview** — parsed tree summary: folder count, file count, total size, and a flagged list (blocked MIME, oversize, empty dir, name collision). Nothing is written yet.
3. **Destination** — radio: New workspace `[name]` | Existing workspace `▾` + optional target folder `▾`; conflict-policy select (default new_version).
4. **Dry-run** — validates the full plan against the destination (name collisions, permission, caps) with **no writes**; renders the exact create/skip plan.
5. **Run** — executes with a concurrency-limited queue and **live per-file progress**; produces a **downloadable report** (created / skipped / failed-with-reason).

## Architecture

### Shared contract (engine-agnostic — the key to phasing)
```ts
type PlannedItem = {
  relPath: string            // sanitized, forward-slash, no leading '/'
  type: 'folder' | 'file'
  sizeBytes?: number
  mimeType?: string
  status: 'ok' | 'blocked_type' | 'oversize' | 'empty' | 'collision'
  note?: string
}
type ImportResult = {
  created: number; skipped: number; failed: number
  items: { relPath: string; outcome: 'created' | 'versioned' | 'skipped' | 'renamed' | 'failed'; reason?: string; documentId?: string }[]
}
```
Both a **client planner** (v1) and a **server planner** (v2) produce `PlannedItem[]` and `ImportResult`; the wizard renders the model regardless of engine.

### v1 — client-side (no backend change)
- Parse the archive in-browser (zip lib) or read the folder drop; build `path → File`.
- **Sanitize every relative path** (reject `..`, absolute paths, drive prefixes → zip-slip guard); normalize separators; drop OS junk (`__MACOSX/`, `.DS_Store`, `Thumbs.db`).
- Resolve destination → ensure workspace exists (create if "new"), then **idempotently create folders** for each directory (reuse existing on name match), caching path→folderId.
- For each file, run the **existing single-upload pipeline** (initiate → presigned PUT → complete). Scan/OCR/dedup/versioning/region_pin all apply because it is the same path. A `p`-limited queue (e.g. 4–6 concurrent) drives progress; per-item failures are recorded and the batch continues.
- Conflict policy applied per file (new_version = complete-as-new-version of the existing doc at that path; skip; rename).

### v2 — server-side (later, additive)
- Upload the zip as one blob → a document-service job unzips + fans out the *same* per-file steps server-side, streaming progress (SSE/websocket or poll). Resumable. Same `PlannedItem`/`ImportResult` shapes, so the wizard is unchanged.

## Reuse vs new

- **Reused:** upload pipeline (initiate/PUT/complete), folder-create, workspace-create, ClamAV scan, OCR, dedup, permission checks, `useAppMutation`/toast conventions.
- **New (v1):** wizard UI (source → preview → destination → dry-run → run → report), client-side archive/folder parser + path sanitizer + concurrency-limited upload queue, the shared `PlannedItem`/`ImportResult` contract.
- **New (v2):** server-side unzip+fan-out job + progress channel (no UI rework).

## Security & edge cases

- **Zip-slip / path traversal** — sanitize + reject `..`/absolute/drive paths before any create.
- **Blocked / oversize files** — the existing scan pipeline rejects (executables, over-cap); mark the item failed and continue.
- **Duplicate names, empty dirs, deep nesting** — surfaced in preview; folders deduped by name; empty dirs skipped.
- **Dedup** — identical SHA links the existing blob (already handled by the pipeline).
- **Partial failure** — v1 records per-item outcome and continues; v2 adds resume.
- **Authz** — admin-gated like today; folder/workspace creation + grants use the caller's permissions.

## Guardrails (YAGNI for v1)

No manifest/CSV, no per-top-folder→workspace split (destination is single new-or-existing), no resumability in v1, no CSV→NDJSON. Soft caps in v1 (warn beyond ~500 MB / ~2,000 files) with a pointer to v2 for larger sets.

## Build phases

- **Phase 1 (v1, ship):** shared contract + client planner + wizard + upload queue; NDJSON demoted to Advanced tab. No backend change.
- **Phase 2 (v2, later):** server-side unzip worker + progress channel behind the same contract.
