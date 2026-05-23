DROP INDEX IF EXISTS idx_document_versions_labeled;
ALTER TABLE document_versions DROP COLUMN IF EXISTS label;
