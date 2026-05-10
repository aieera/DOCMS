-- ADR 0075 — bulk import idempotency + external_id <-> internal UUID
-- mapping. The bulk import surface accepts the tenant's natural key
-- (`external_id`) for every row and resolves it into an internal
-- UUID via this mapping table. Re-imports become no-ops; cross-
-- references inside one stream (folder.workspace_external_id,
-- document.folder_external_id, user.group_external_ids) all resolve
-- through the same map.

-- ---------------------------------------------------------------------------
-- 1. bulk_external_id_map
-- ---------------------------------------------------------------------------
-- Composite key on (tenant_id, resource_type, external_id) is the natural
-- lookup. resource_type is one of: workspace, folder, document, user, group.
CREATE TABLE bulk_external_id_map (
    tenant_id     UUID         NOT NULL REFERENCES organizations(id),
    resource_type TEXT         NOT NULL CHECK (resource_type IN
                                                ('workspace','folder','document','user','group')),
    external_id   TEXT         NOT NULL,
    internal_id   UUID         NOT NULL,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, resource_type, external_id)
);
CREATE INDEX idx_bulk_external_id_map_internal
    ON bulk_external_id_map(tenant_id, resource_type, internal_id);
ALTER TABLE bulk_external_id_map ENABLE ROW LEVEL SECURITY;
ALTER TABLE bulk_external_id_map FORCE  ROW LEVEL SECURITY;
CREATE POLICY bulk_xid_tenant_isolation ON bulk_external_id_map
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY bulk_xid_tenant_isolation_insert ON bulk_external_id_map
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ---------------------------------------------------------------------------
-- 2. bulk_import_log
-- ---------------------------------------------------------------------------
-- Per-request idempotency. The client mints a request_id (UUID v7) for
-- every BulkImportRequest batch; the server stores the result and replays
-- the cached status on retry. Includes a digest of the items so an
-- accidental id-collision with different content is detectable.
CREATE TABLE bulk_import_log (
    tenant_id    UUID         NOT NULL REFERENCES organizations(id),
    request_id   UUID         NOT NULL,
    items_digest TEXT         NOT NULL, -- sha256 of canonicalized items
    status       TEXT         NOT NULL DEFAULT 'queued'
                                  CHECK (status IN ('queued','succeeded','partial','failed')),
    item_count   INT          NOT NULL,
    success_count INT         NOT NULL DEFAULT 0,
    failure_count INT         NOT NULL DEFAULT 0,
    response_json JSONB,
    started_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, request_id)
);
CREATE INDEX idx_bulk_import_log_started
    ON bulk_import_log(tenant_id, started_at DESC);
ALTER TABLE bulk_import_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE bulk_import_log FORCE  ROW LEVEL SECURITY;
CREATE POLICY bulk_log_tenant_isolation ON bulk_import_log
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY bulk_log_tenant_isolation_insert ON bulk_import_log
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
