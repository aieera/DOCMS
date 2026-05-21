-- ADR 0115 Phase 1 rollback.
--
-- Order matters: drop the dependent table first (FK + RLS), then the
-- documents column. Drops are idempotent so a partial re-run cleans up.

DROP TABLE IF EXISTS document_processing_stages CASCADE;

DROP INDEX IF EXISTS idx_documents_processing_status_failed;

ALTER TABLE documents
    DROP CONSTRAINT IF EXISTS documents_processing_status_check;

ALTER TABLE documents
    DROP COLUMN IF EXISTS processing_status;
