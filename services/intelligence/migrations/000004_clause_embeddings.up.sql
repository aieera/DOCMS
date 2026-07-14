-- ADR 0104 Phase 2 — per-clause embedding cache. Self-healing: the
-- detect_clauses task re-embeds any clause whose body_sha256 differs
-- from the cached row (covers pre-existing clauses, no backfill).
-- real[] not pgvector: the vector is read back into Python for a
-- Qdrant query, never searched in SQL.
CREATE TABLE IF NOT EXISTS clause_embeddings (
    tenant_id   uuid        NOT NULL,
    clause_id   uuid        NOT NULL,
    body_sha256 text        NOT NULL,
    vector      real[]      NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, clause_id)
);

ALTER TABLE clause_embeddings ENABLE ROW LEVEL SECURITY;
ALTER TABLE clause_embeddings FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS clause_embeddings_tenant_isolation ON clause_embeddings;
CREATE POLICY clause_embeddings_tenant_isolation ON clause_embeddings
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
