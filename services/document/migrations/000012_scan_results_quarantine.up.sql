-- Migration 000012 — scan_results + quarantine_events
--
-- Closes the gap flagged by the Blueprint §22 anti-pattern audit: the
-- storage service was INSERTing into scan_results but the table had never
-- been created. quarantine_events is the audit trail for every time we
-- move an object to the quarantine bucket (infected OR blocked-MIME).
--
-- Both tables are per-tenant with RLS (matches the pattern used across
-- the document-service schema this migration sits in).

CREATE TABLE scan_results (
    tenant_id   UUID NOT NULL REFERENCES organizations(id),
    id          UUID NOT NULL DEFAULT gen_random_uuid(),
    upload_id   UUID NOT NULL,
    result      TEXT NOT NULL CHECK (result IN ('clean', 'infected', 'pending', 'error')),
    signature   TEXT,
    scanned_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, upload_id) REFERENCES upload_sessions(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_scan_results_upload  ON scan_results(tenant_id, upload_id, scanned_at DESC);
-- Partial index powers the pending-reconciliation sweep (see reconcile.go).
CREATE INDEX idx_scan_results_pending ON scan_results(scanned_at)
    WHERE result = 'pending';
ALTER TABLE scan_results ENABLE ROW LEVEL SECURITY;
ALTER TABLE scan_results FORCE  ROW LEVEL SECURITY;
CREATE POLICY scan_results_tenant_isolation ON scan_results
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY scan_results_tenant_isolation_insert ON scan_results
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE quarantine_events (
    tenant_id      UUID NOT NULL REFERENCES organizations(id),
    id             UUID NOT NULL DEFAULT gen_random_uuid(),
    upload_id      UUID NOT NULL,
    reason         TEXT NOT NULL CHECK (reason IN ('virus', 'blocked_mime', 'mime_mismatch')),
    signature      TEXT,           -- virus name OR detected MIME
    declared_mime  TEXT,
    detected_mime  TEXT,
    storage_bucket TEXT NOT NULL,  -- quarantine bucket + key at rest
    storage_key    TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, upload_id) REFERENCES upload_sessions(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_quarantine_events_upload  ON quarantine_events(tenant_id, upload_id);
CREATE INDEX idx_quarantine_events_created ON quarantine_events(tenant_id, created_at DESC);
ALTER TABLE quarantine_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE quarantine_events FORCE  ROW LEVEL SECURITY;
CREATE POLICY quarantine_events_tenant_isolation ON quarantine_events
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY quarantine_events_tenant_isolation_insert ON quarantine_events
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
