-- ===========================================================================
-- TABLE: scan_results
-- ===========================================================================
-- Audit log of ClamAV virus-scan outcomes, written by the storage service's
-- CompleteUpload path (services/storage/internal/repository/scans.go). The
-- table was referenced by the code but never had a migration, so CompleteUpload
-- failed with `relation "scan_results" does not exist` and every upload 500'd.
--
-- upload_id is the storage upload-session UUID (not a FK — upload sessions are
-- not persisted as their own table). Same tenant-first PK + forced-RLS
-- convention as content_blobs / documents.
CREATE TABLE scan_results (
    tenant_id  UUID NOT NULL REFERENCES organizations(id),
    id         UUID NOT NULL DEFAULT gen_random_uuid(),
    upload_id  UUID NOT NULL,
    result     TEXT NOT NULL
                    CHECK (result IN ('clean', 'infected', 'pending', 'error')),
    signature  TEXT,
    scanned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);

-- GetByUpload reads the latest scan for a given upload:
--   WHERE tenant_id = $1 AND upload_id = $2 ORDER BY scanned_at DESC LIMIT 1
CREATE INDEX idx_scan_results_upload
    ON scan_results(tenant_id, upload_id, scanned_at DESC);

ALTER TABLE scan_results ENABLE ROW LEVEL SECURITY;
ALTER TABLE scan_results FORCE  ROW LEVEL SECURITY;
CREATE POLICY scan_results_tenant_isolation ON scan_results
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY scan_results_tenant_isolation_insert ON scan_results
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
