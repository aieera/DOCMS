-- Two-tier trash.
--
-- Before this, a soft-deleted item lived in exactly one place: the
-- tenant-wide admin Trash. Members could not see or recover anything they
-- deleted, and the only "delete permanently" was an admin-run purge that
-- dropped the S3 objects and the row — irreversible.
--
-- The model now has three states, not two:
--
--   deleted_at IS NULL                      → live
--   deleted_at SET, user_cleared_at NULL    → in the deleter's Trash AND
--                                             in admin Trash
--   deleted_at SET, user_cleared_at SET     → cleared from the deleter's
--                                             Trash, still in admin Trash
--                                             and still restorable
--
-- Only the admin purge (S3 + row delete) remains terminal. user_cleared_at
-- is deliberately NOT a delete: it hides the row from one person's view.

ALTER TABLE documents
    ADD COLUMN IF NOT EXISTS user_cleared_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS user_cleared_by UUID;

ALTER TABLE folders
    ADD COLUMN IF NOT EXISTS user_cleared_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS user_cleared_by UUID;

-- "My trash" is (tenant, deleter) scoped over soft-deleted rows only, so a
-- partial index on deleted rows keeps it off the live-document hot path.
CREATE INDEX IF NOT EXISTS idx_documents_user_trash
    ON documents (tenant_id, deleted_by, deleted_at DESC)
    WHERE deleted_at IS NOT NULL AND user_cleared_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_folders_user_trash
    ON folders (tenant_id, deleted_by, deleted_at DESC)
    WHERE deleted_at IS NOT NULL AND user_cleared_at IS NULL;
