-- Classification corrections (ADR 0059) — append-only ledger of every
-- "the model said X, the user said Y" event. Drives the active-learning
-- training loop (ADR 0060) and powers the bulk-reclassify admin UI.

CREATE TABLE classification_corrections (
    tenant_id            UUID        NOT NULL REFERENCES organizations(id),
    id                   UUID        NOT NULL DEFAULT gen_random_uuid(),
    document_id          UUID        NOT NULL,
    version_id           UUID        NOT NULL,
    original_category    TEXT        NOT NULL,         -- model's prediction (or empty if unclassified)
    corrected_category   TEXT        NOT NULL,         -- ground truth from the user
    original_confidence  REAL,                         -- model's confidence on the original prediction
    correction_source    TEXT        NOT NULL DEFAULT 'manual'
                                     CHECK (correction_source IN ('manual','bulk','api')),
    note                 TEXT,
    corrected_by         UUID        NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, document_id) REFERENCES documents         (tenant_id, id),
    FOREIGN KEY (tenant_id, version_id)  REFERENCES document_versions (tenant_id, id),
    FOREIGN KEY (tenant_id, corrected_by) REFERENCES users            (tenant_id, id)
);

-- A user can correct the same doc multiple times (re-correction); we
-- keep the full history. Newest-first lookup is the common access.
CREATE INDEX idx_classify_corrections_doc
    ON classification_corrections (tenant_id, document_id, created_at DESC);
CREATE INDEX idx_classify_corrections_user
    ON classification_corrections (tenant_id, corrected_by, created_at DESC);
-- Retrain trigger reads "all corrections for this tenant since last train"
-- — covered by the (tenant, created_at) leading index above.

ALTER TABLE classification_corrections ENABLE ROW LEVEL SECURITY;
ALTER TABLE classification_corrections FORCE  ROW LEVEL SECURITY;
CREATE POLICY classify_corrections_tenant_isolation ON classification_corrections
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY classify_corrections_tenant_isolation_insert ON classification_corrections
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
