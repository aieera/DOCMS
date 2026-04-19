BEGIN;

ALTER TABLE document_versions RENAME TO versions;

ALTER TABLE folders DROP CONSTRAINT IF EXISTS folders_updated_by_fkey;
ALTER TABLE folders DROP COLUMN IF EXISTS updated_by;

ALTER TABLE documents DROP CONSTRAINT IF EXISTS documents_updated_by_fkey;
ALTER TABLE documents
    DROP COLUMN IF EXISTS version_count,
    DROP COLUMN IF EXISTS under_legal_hold,
    DROP COLUMN IF EXISTS updated_by;

COMMIT;
