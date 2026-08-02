DROP INDEX IF EXISTS idx_folders_user_trash;
DROP INDEX IF EXISTS idx_documents_user_trash;

ALTER TABLE folders
    DROP COLUMN IF EXISTS user_cleared_by,
    DROP COLUMN IF EXISTS user_cleared_at;

ALTER TABLE documents
    DROP COLUMN IF EXISTS user_cleared_by,
    DROP COLUMN IF EXISTS user_cleared_at;
