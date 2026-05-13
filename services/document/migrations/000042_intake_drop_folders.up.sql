-- ADR 0088 — server-side watched-folder intake.
--
-- Two tables, both per-tenant + RLS-isolated:
--   * intake_drop_folders   per-tenant folder config + status.
--   * intake_ingested_files audit log + dedup key.

CREATE TABLE IF NOT EXISTS intake_drop_folders (
    tenant_id            UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    id                   UUID NOT NULL DEFAULT gen_random_uuid(),

    label                TEXT NOT NULL,
    active               BOOLEAN NOT NULL DEFAULT TRUE,
    host_path            TEXT NOT NULL,           -- absolute path inside connector container
    target_workspace_id  UUID,
    target_folder_id     UUID,

    quarantine_subdir    TEXT NOT NULL DEFAULT 'quarantine',
    processed_subdir     TEXT NOT NULL DEFAULT 'processed',
    recurse              BOOLEAN NOT NULL DEFAULT FALSE,
    extensions_csv       TEXT NOT NULL DEFAULT '',  -- empty = any; csv "pdf,tiff,jpg"

    last_seen_at         TIMESTAMPTZ,
    files_ingested       BIGINT NOT NULL DEFAULT 0,
    last_error           TEXT,

    created_by           UUID,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, host_path)
);

ALTER TABLE intake_drop_folders ENABLE ROW LEVEL SECURITY;
ALTER TABLE intake_drop_folders FORCE ROW LEVEL SECURITY;
CREATE POLICY intake_drop_folders_tenant_isolation ON intake_drop_folders
    USING (tenant_id = (current_setting('app.current_tenant', true))::uuid)
    WITH CHECK (tenant_id = (current_setting('app.current_tenant', true))::uuid);

CREATE INDEX IF NOT EXISTS idx_intake_drop_folders_active
    ON intake_drop_folders (tenant_id)
    WHERE active = TRUE;

GRANT SELECT, INSERT, UPDATE, DELETE ON intake_drop_folders TO dms_app;


CREATE TABLE IF NOT EXISTS intake_ingested_files (
    tenant_id     UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    id            UUID NOT NULL DEFAULT gen_random_uuid(),
    folder_id     UUID NOT NULL,

    source_path   TEXT NOT NULL,    -- relative to the config's host_path
    sha256        TEXT NOT NULL,
    size_bytes    BIGINT NOT NULL,

    document_id   UUID,             -- NULL until materialised

    ingest_status TEXT NOT NULL DEFAULT 'pending'
                  CHECK (ingest_status IN ('pending', 'ingested', 'failed', 'quarantined')),
    ingest_error  TEXT,

    ingested_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    -- Idempotency: same bytes dropped twice = one row.
    UNIQUE (tenant_id, folder_id, sha256),

    FOREIGN KEY (tenant_id, folder_id)
        REFERENCES intake_drop_folders(tenant_id, id) ON DELETE CASCADE
);

ALTER TABLE intake_ingested_files ENABLE ROW LEVEL SECURITY;
ALTER TABLE intake_ingested_files FORCE ROW LEVEL SECURITY;
CREATE POLICY intake_ingested_files_tenant_isolation ON intake_ingested_files
    USING (tenant_id = (current_setting('app.current_tenant', true))::uuid)
    WITH CHECK (tenant_id = (current_setting('app.current_tenant', true))::uuid);

CREATE INDEX IF NOT EXISTS idx_intake_ingested_recent
    ON intake_ingested_files (tenant_id, folder_id, ingested_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON intake_ingested_files TO dms_app;
