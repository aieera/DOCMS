-- Async e-discovery export jobs (§9.5 / G9 extension). A hold-scoped export is
-- built in the background, staged to object storage, and downloaded when ready.
CREATE TABLE export_jobs (
    tenant_id      UUID NOT NULL REFERENCES organizations(id),
    id             UUID NOT NULL DEFAULT gen_random_uuid(),
    hold_id        UUID,                       -- the legal hold scoping the export
    query          TEXT,                       -- optional title/metadata refinement
    format         TEXT NOT NULL DEFAULT 'edrm',
    status         TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'running', 'completed', 'failed')),
    doc_count      INTEGER NOT NULL DEFAULT 0,
    size_bytes     BIGINT  NOT NULL DEFAULT 0, -- size of the produced export
    storage_bucket TEXT,
    storage_key    TEXT,
    error          TEXT,
    requested_by   UUID,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at   TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id)
);
CREATE INDEX idx_export_jobs_status ON export_jobs(tenant_id, status, created_at DESC);
ALTER TABLE export_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE export_jobs FORCE  ROW LEVEL SECURITY;
CREATE POLICY export_jobs_tenant_isolation ON export_jobs
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY export_jobs_tenant_isolation_insert ON export_jobs
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
