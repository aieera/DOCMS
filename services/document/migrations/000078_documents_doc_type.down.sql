DROP INDEX IF EXISTS idx_documents_doc_type;
ALTER TABLE documents DROP COLUMN IF EXISTS doc_type;
