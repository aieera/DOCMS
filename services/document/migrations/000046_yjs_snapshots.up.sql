-- ADR 0096 — Yjs CRDT snapshot persistence.
--
-- The collaboration service holds the live Y.Doc in memory per (tenant, doc)
-- room. Without this table, state is lost when the last client leaves OR
-- the worker restarts. Each flush stores the full encoded state (not
-- deltas) — simpler, and we keep only the last N per (tenant, doc) so
-- storage stays bounded.
--
-- Tenant isolation: RLS, NOBYPASSRLS app role.
CREATE TABLE IF NOT EXISTS yjs_snapshots (
    tenant_id   uuid        NOT NULL REFERENCES organizations(id),
    doc_id      uuid        NOT NULL,
    update_seq  bigint      NOT NULL,
    state_bin   bytea       NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, doc_id, update_seq)
);

CREATE INDEX IF NOT EXISTS idx_yjs_snapshots_recent
    ON yjs_snapshots (tenant_id, doc_id, update_seq DESC);

-- RLS — every query must be tenant-scoped via app.current_tenant GUC.
ALTER TABLE yjs_snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE yjs_snapshots FORCE ROW LEVEL SECURITY;

CREATE POLICY yjs_snapshots_tenant_isolation ON yjs_snapshots
    USING (tenant_id::text = current_setting('app.current_tenant', true))
    WITH CHECK (tenant_id::text = current_setting('app.current_tenant', true));
