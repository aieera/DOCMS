-- Wave 8 Prompt 8.4: residency migration tracking.
--
-- The migrate-documents workflow must be resumable and idempotent:
-- a worker crash mid-sweep must resume without re-moving already-
-- moved docs, and re-submitting the same migration must be a no-op.
--
-- `residency_migrations` is the operational row per workflow run.
-- `residency_migration_items` tracks per-document progress so the
-- workflow can pick up where it left off.
--
-- Index plan:
--   residency_migrations:
--     PK (tenant_id, id) — hot row lookup.
--     idx on (tenant_id, status, created_at DESC) — admin list.
--   residency_migration_items:
--     PK (migration_id, document_id) — per-doc dedupe.
--     idx on (migration_id, status) — resume query: find pending items.
BEGIN;

CREATE TABLE residency_migrations (
    tenant_id         UUID NOT NULL REFERENCES organizations(id),
    id                UUID NOT NULL DEFAULT gen_random_uuid(),
    source_region     TEXT NOT NULL,
    target_region     TEXT NOT NULL CHECK (target_region <> source_region),
    filter_workspace  UUID,
    filter_document_class TEXT,
    status            TEXT NOT NULL DEFAULT 'pending'
                          CHECK (status IN ('pending','running','completed','failed','cancelled')),
    total_docs        INT NOT NULL DEFAULT 0,
    moved_docs        INT NOT NULL DEFAULT 0,
    failed_docs       INT NOT NULL DEFAULT 0,
    initiated_by      UUID,
    workflow_run_id   TEXT,
    error_summary     TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at      TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, initiated_by) REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_residency_migrations_tenant_status
    ON residency_migrations(tenant_id, status, created_at DESC);

ALTER TABLE residency_migrations ENABLE ROW LEVEL SECURITY;
ALTER TABLE residency_migrations FORCE ROW LEVEL SECURITY;
CREATE POLICY residency_migrations_tenant_isolation ON residency_migrations
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY residency_migrations_tenant_isolation_insert ON residency_migrations
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE residency_migration_items (
    migration_id    UUID NOT NULL,
    tenant_id       UUID NOT NULL,
    document_id     UUID NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending'
                          CHECK (status IN ('pending','moved','failed','skipped')),
    error_message   TEXT,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (migration_id, document_id),
    FOREIGN KEY (tenant_id, migration_id) REFERENCES residency_migrations(tenant_id, id)
);
CREATE INDEX idx_residency_items_resume
    ON residency_migration_items(migration_id, status);

ALTER TABLE residency_migration_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE residency_migration_items FORCE ROW LEVEL SECURITY;
CREATE POLICY residency_items_tenant_isolation ON residency_migration_items
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

COMMIT;
