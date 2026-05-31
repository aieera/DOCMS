-- FIX-5 (audit Section 11): folder cascade-delete + restore.
--
-- The previous DeleteFolder refused non-empty folders outright, with
-- no restore path even for the trivial case. Drive/Box/SharePoint/
-- M-Files all ship folder soft-delete that cascades to documents +
-- sub-folders, and a Recycle Bin restore that brings the entire
-- subtree back. This migration adds the two columns needed for that
-- model on both `folders` and `documents`:
--
--   deleted_cohort_id  — groups every folder + document that was
--                        soft-deleted in the SAME DeleteFolder call.
--                        RestoreFolder reads this id from the root
--                        and restores every row matching it. Without
--                        the cohort we'd over-restore (any other
--                        descendant that happened to be deleted
--                        independently would come back too).
--   deleted_by         — actor UUID. The previous schema only carried
--                        the actor in the outbox payload, which made
--                        the "who deleted this?" question a NATS-log
--                        spelunking exercise.
--
-- Both columns are nullable: pre-existing soft-deletes (deleted_at IS
-- NOT NULL) keep working without backfill. Restore on those rows
-- isn't supported — they have no cohort to scope by — but that
-- matches today's reality where there's no restore at all.

ALTER TABLE folders
    ADD COLUMN IF NOT EXISTS deleted_cohort_id UUID NULL,
    ADD COLUMN IF NOT EXISTS deleted_by        UUID NULL;

ALTER TABLE documents
    ADD COLUMN IF NOT EXISTS deleted_cohort_id UUID NULL,
    ADD COLUMN IF NOT EXISTS deleted_by        UUID NULL;

-- Partial indexes so the Restore lookup ("every row in cohort X")
-- doesn't scan the live table. Predicate-on-cohort matches the
-- exact write pattern: a row joins the index only when it's part of
-- a recoverable subtree.
CREATE INDEX IF NOT EXISTS idx_folders_deleted_cohort
    ON folders(tenant_id, deleted_cohort_id)
    WHERE deleted_cohort_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_documents_deleted_cohort
    ON documents(tenant_id, deleted_cohort_id)
    WHERE deleted_cohort_id IS NOT NULL;
