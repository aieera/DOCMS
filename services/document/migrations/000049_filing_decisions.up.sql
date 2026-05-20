-- ADR 0102 — Predictive filing: append-only decisions ledger.
-- Becomes the training corpus for the Phase 2 ML model that replaces
-- the Phase 1 frequency heuristic. Schema chosen for training
-- friendliness (booleans for easy WHERE filters, raw context kept
-- for future features).

CREATE TABLE IF NOT EXISTS filing_decisions (
    tenant_id              uuid        NOT NULL REFERENCES organizations(id),
    id                     uuid        NOT NULL DEFAULT gen_random_uuid(),
    user_id                uuid        NOT NULL,
    prediction_id          uuid        NOT NULL,                       -- groups predict + feedback pair
    -- Predicted values (what the system suggested)
    predicted_class        text,
    predicted_class_score  real,
    predicted_folder_id    uuid,
    predicted_folder_score real,
    predicted_tags         text[]      NOT NULL DEFAULT '{}',
    -- Final values (what the user actually picked)
    final_class            text,
    final_folder_id        uuid,
    final_tags             text[]      NOT NULL DEFAULT '{}',
    -- Booleans for the easy training-data extract
    class_accepted         boolean     NOT NULL,
    folder_accepted        boolean     NOT NULL,
    tags_accept_count      integer     NOT NULL DEFAULT 0,
    tags_reject_count      integer     NOT NULL DEFAULT 0,
    -- Context for the future model
    filename               text        NOT NULL DEFAULT '',
    mime_type              text        NOT NULL DEFAULT '',
    workspace_id           uuid,
    created_at             timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);

CREATE INDEX IF NOT EXISTS idx_filing_decisions_corpus
    ON filing_decisions (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_filing_decisions_user
    ON filing_decisions (tenant_id, user_id, created_at DESC);
-- Find the prediction row by prediction_id so feedback POSTs can
-- update without scanning. UUID is globally unique → tenant_id pair
-- not strictly needed but keeps the partial RLS shape consistent.
CREATE UNIQUE INDEX IF NOT EXISTS uq_filing_decisions_prediction
    ON filing_decisions (tenant_id, prediction_id);

ALTER TABLE filing_decisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE filing_decisions FORCE ROW LEVEL SECURITY;
CREATE POLICY filing_decisions_tenant_isolation ON filing_decisions
    USING (tenant_id::text = current_setting('app.current_tenant', true))
    WITH CHECK (tenant_id::text = current_setting('app.current_tenant', true));
