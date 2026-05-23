-- Phase 5 — retention scope rollback.

DROP INDEX IF EXISTS idx_documents_class_lifecycle;
DROP INDEX IF EXISTS idx_documents_folder_lifecycle;
DROP INDEX IF EXISTS idx_documents_workspace_lifecycle;
DROP INDEX IF EXISTS idx_documents_retention_exempt;

ALTER TABLE documents
    DROP COLUMN IF EXISTS retention_exempt_set_at,
    DROP COLUMN IF EXISTS retention_exempt_set_by,
    DROP COLUMN IF EXISTS retention_exempt_reason,
    DROP COLUMN IF EXISTS retention_exempt;

ALTER TABLE retention_policies
    DROP CONSTRAINT IF EXISTS retention_policies_folder_filter_fk;
ALTER TABLE retention_policies
    DROP COLUMN IF EXISTS folder_filter;
