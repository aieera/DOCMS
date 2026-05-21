-- Support index for the document-service orphan GC (see
-- services/document/internal/janitor/orphan_gc.go).
--
-- An "orphan" is a documents row with NO version attached yet — the
-- frontend creates the row, then calls initiate-upload + S3 PUT +
-- complete-upload. If the client crashes between create and complete,
-- the row sits forever with current_version_id IS NULL, taking a slot
-- in folder listings (filtered out client-side, but still costing DB
-- rows + index space).
--
-- The janitor scans this index every hour and soft-deletes rows older
-- than 24h, giving real uploads enough headroom to complete on slow
-- networks.
--
-- Partial WHERE keeps the index tiny — under healthy conditions it
-- only holds orphans currently in their 24h grace window.
--
-- Non-CONCURRENT: golang-migrate wraps every file in a transaction,
-- and CREATE INDEX CONCURRENTLY cannot run inside one. The set this
-- indexes is small (only orphans, not the full documents table), so
-- the brief ShareLock during creation is acceptable. Operators with
-- already-huge tenants who want zero-lock can apply the index
-- out-of-band first, then run migrate (IF NOT EXISTS is a no-op).
CREATE INDEX IF NOT EXISTS idx_documents_orphans
    ON documents (created_at)
    WHERE current_version_id IS NULL AND deleted_at IS NULL;
