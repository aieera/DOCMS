-- §4.1 / A4 / C6 — reconcile document-service schema with shipped repository
-- code. The repo reads/writes four columns the initial schema never created:
--
--   documents.updated_by         — written by Update() and Create()
--   documents.under_legal_hold   — written by Update() and UpdateLifecycleState()
--   documents.version_count      — written by Create() and SetCurrentVersion()
--   folders.updated_by           — written by folder Create()
--
-- This is the analogous reconcile to billing's 000010 from Wave 10. The
-- columns are additive and backfilled with safe defaults; no row migration
-- is required. Addressed together here because no integration test of the
-- document service can run (including §5.1 / A1's version-uploaded event
-- test) until every write path the SUT touches has a column to land in.

BEGIN;

ALTER TABLE documents
    ADD COLUMN IF NOT EXISTS updated_by       UUID,
    ADD COLUMN IF NOT EXISTS under_legal_hold BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS version_count    INT     NOT NULL DEFAULT 0;

-- FK is composite (tenant_id, updated_by) → users(tenant_id, id), matching
-- the convention established by created_by in the initial schema.
ALTER TABLE documents
    ADD CONSTRAINT documents_updated_by_fkey
    FOREIGN KEY (tenant_id, updated_by) REFERENCES users(tenant_id, id);

ALTER TABLE folders
    ADD COLUMN IF NOT EXISTS updated_by UUID;

ALTER TABLE folders
    ADD CONSTRAINT folders_updated_by_fkey
    FOREIGN KEY (tenant_id, updated_by) REFERENCES users(tenant_id, id);

-- The initial schema named the version table `versions`, but the repository
-- layer and the intelligence service's nats_consumer both query
-- `document_versions`. Rename to the caller-canonical name; FK constraints,
-- indexes and RLS policies survive ALTER TABLE ... RENAME.
ALTER TABLE versions RENAME TO document_versions;

COMMIT;
