-- Wave 11.5: document redaction audit log.
--
-- Redaction is a compliance-relevant operation: regulators want a
-- permanent record of which fields were redacted, when, by whom,
-- and for which reason. We persist that metadata in a tenant-
-- scoped audit table. Actual pixel-level redaction (PyMuPDF
-- apply_redactions) still lives in the intelligence service as a
-- Celery task; this table records the decision, the orchestrator
-- records the outcome.
--
-- Rows are append-only — redactions cannot be undone because the
-- underlying PDF has been re-encoded with the masked regions
-- flattened.
--
-- Index plan:
--   PK (tenant_id, id) — composite tenant scope.
--   idx on (tenant_id, document_id, created_at DESC) — audit
--     lookup "show every redaction on this doc, most recent first."
BEGIN;

CREATE TABLE document_redactions (
    tenant_id        UUID NOT NULL REFERENCES organizations(id),
    id               UUID NOT NULL DEFAULT gen_random_uuid(),
    document_id      UUID NOT NULL,
    version_id       UUID,
    applied_by       UUID,
    reason           TEXT NOT NULL,
    regions          JSONB NOT NULL DEFAULT '[]'::jsonb,
    entity_types     TEXT[] NOT NULL DEFAULT '{}',
    status           TEXT NOT NULL DEFAULT 'queued'
                        CHECK (status IN ('queued','applied','failed')),
    intelligence_task_id TEXT,
    error_message    TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at     TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, document_id) REFERENCES documents(tenant_id, id),
    FOREIGN KEY (tenant_id, applied_by)  REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_doc_redactions_doc
    ON document_redactions(tenant_id, document_id, created_at DESC);

ALTER TABLE document_redactions ENABLE ROW LEVEL SECURITY;
ALTER TABLE document_redactions FORCE ROW LEVEL SECURITY;
CREATE POLICY doc_redactions_tenant_isolation ON document_redactions
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY doc_redactions_tenant_isolation_insert ON document_redactions
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

COMMIT;
