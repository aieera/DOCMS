-- ADR 0104 Phase 2 — clause detection results. Written by the
-- intelligence detect_clauses task (shared DB, same pattern as
-- extracted_fields), read by the document service panel + variations
-- endpoints. Idempotent per (tenant, document, version): the task
-- deletes then re-inserts.
CREATE TABLE IF NOT EXISTS clause_matches (
    tenant_id    uuid        NOT NULL REFERENCES organizations(id),
    id           uuid        NOT NULL DEFAULT gen_random_uuid(),
    document_id  uuid        NOT NULL,
    version_id   uuid        NOT NULL,
    clause_id    uuid        NOT NULL,
    similarity   real        NOT NULL,
    chunk_index  integer     NOT NULL DEFAULT 0,
    matched_text text        NOT NULL DEFAULT '',
    detected_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, clause_id) REFERENCES clauses(tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, document_id) REFERENCES documents(tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_clause_matches_doc
    ON clause_matches (tenant_id, document_id, version_id);
CREATE INDEX IF NOT EXISTS idx_clause_matches_clause
    ON clause_matches (tenant_id, clause_id, detected_at DESC);

ALTER TABLE clause_matches ENABLE ROW LEVEL SECURITY;
ALTER TABLE clause_matches FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS clause_matches_tenant_isolation ON clause_matches;
CREATE POLICY clause_matches_tenant_isolation ON clause_matches
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
