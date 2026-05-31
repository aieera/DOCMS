DROP INDEX IF EXISTS idx_documents_deleted_cohort;
DROP INDEX IF EXISTS idx_folders_deleted_cohort;

ALTER TABLE documents
    DROP COLUMN IF EXISTS deleted_by,
    DROP COLUMN IF EXISTS deleted_cohort_id;

ALTER TABLE folders
    DROP COLUMN IF EXISTS deleted_by,
    DROP COLUMN IF EXISTS deleted_cohort_id;
