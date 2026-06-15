DROP INDEX IF EXISTS idx_documents_external_id;
ALTER TABLE documents DROP COLUMN IF EXISTS external_id;
