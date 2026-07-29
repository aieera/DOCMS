# Trash: folder permanent delete + empty trash — design

Date: 2026-07-28
Status: approved for implementation (autonomous session; user asked for a "proper delete system" with permanent delete from trash)

## Problem

The Trash page lists soft-deleted folders (cohort roots) and documents. Documents
already support per-item restore and permanent delete. Folders only support
restore: there is no `PurgeFolder` service method, no repo hard-delete for
folders, and no HTTP endpoint. Soft-deleted folder rows are immortal. There is
also no "empty trash" bulk action, single-document deletes do not stamp
`deleted_by` (blank "Deleted by" column), and the web trash list's "Load more"
replaces the page instead of appending.

## Scope

### Backend (services/document)

1. **`PurgeFolder(ctx, folderID, userID)`** in `internal/service/folders.go`.
   - Folder must be soft-deleted (400 otherwise, mirroring `PurgeDocument`).
   - Resolve the delete cohort. For legacy rows with `deleted_cohort_id IS NULL`,
     purge the folder's soft-deleted subtree by ltree path instead (these rows
     are otherwise unremovable).
   - For every document in the cohort/subtree, run the same guards as
     `PurgeDocument`: active legal hold or unelapsed retention ⇒ 409 Conflict
     listing blockers. Fail closed: if any document is blocked, nothing is purged.
   - Three phases like `PurgeDocument`: (1) tx — validate + collect blobs,
     (2) S3 `DeleteObject` per blob (tolerate NoSuchKey), (3) tx — delete
     version/blob rows, hard-delete documents, hard-delete folder rows, insert
     outbox `dms.folder.purged.v1` (folder_id, cohort_id, purged_by, counts).
2. **`EmptyTrash(ctx, userID)`** in the same service.
   - Purge every trashed document via the existing per-document purge logic and
     every trashed folder cohort via `PurgeFolder` logic; skip (do not fail on)
     blocked items; return `{purged_documents, purged_folders, skipped: [{id, type, reason}]}`.
3. **Routes** in `trash_handler.go` (registered from `main.go` in the existing
   admin-trash block, `requireRole(owner, admin)`):
   - `DELETE /api/v1/admin/trash/folders/{id}` → purge folder
   - `DELETE /api/v1/admin/trash` → empty trash
   Gateway `routes.yaml` already allowlists the `/api/v1/admin/trash` prefix.
4. **Repo additions** (`folder_repo.go` / `document_repo.go` as needed):
   cohort document listing, folder hard-delete by cohort / by subtree path.
5. **Events**: add `dms.folder.purged.v1` and register any missing trash
   subjects in `pkg/events/coverage.go` so the coverage gate passes.
6. **`deleted_by` stamping**: `Documents.SoftDelete` records the acting user;
   trash listing (`trashEntry`) exposes `deleted_by`.
7. **Tests**: service-level tests for the purge guards (pattern of
   `trash_retention_test.go`) + handler-level tests for the new routes.

### Frontend (web)

1. `api/trash.ts`: `purgeFolderFromTrash(id)`, `emptyTrash()`, `deleted_by` on
   `TrashEntry`.
2. `routes/_authenticated/trash.tsx`:
   - Per-folder "Delete permanently" button using the existing
     `TypedConfirmDialog` (type the folder name to confirm), destructive style,
     disabled reasoning consistent with docs.
   - "Empty trash" button in the `PageHeader` actions slot, `TypedConfirmDialog`
     (type EMPTY), summarising counts; toast reports purged/skipped.
   - Fix "Load more": accumulate pages instead of replacing `pageToken`.
   - Show `deleted_by` for documents.
   - Query invalidation for `admin-trash`, `admin-trash-folders`, `documents`,
     `folders`.

## Out of scope (documented gaps, separate efforts)

- Retention-based auto-purge of trash ("permanently delete after N days").
- User-scoped (non-admin) trash.
- Search reindex on restore (`dms.*.restored.v1` consumers).
- Legal-hold enforcement inside the *soft* cascade delete (the purge path now
  guards it, which closes the destructive half).
- Crypto-shredding via `content_blobs.shredded_at` (currently write-never).

## Error handling

- Purge folder: 404 unknown/not-deleted id in tenant; 400 not soft-deleted;
  409 with `blockers` payload when any cohort document is under hold/retention;
  502-ish storage errors abort before any row deletion (S3 phase precedes DB
  hard delete, and re-runs tolerate already-deleted objects).
- Empty trash never 409s: blocked items are reported in `skipped`.
