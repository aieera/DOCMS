-- Migration 000017 — DSR augmentation (ADR 0037).
--
-- Builds on ADR 0024's privacy_dsr_requests + privacy_ledger.
-- Adds:
--   - public-form intake: status_token_hash + intake_source on
--     privacy_dsr_requests, plus requester_identity_verified_at for
--     the magic-link confirmation
--   - dsr_request_artifacts: multi-artifact support (export ZIP +
--     rectification diff + erasure report). The legacy single-column
--     export_url stays for back-compat; new code reads the artifact
--     table.
--   - dsr_conflicts: structured surface for legal-hold conflicts that
--     today live as free text in blocked_reason. Compliance officers
--     iterate this table to resolve; the existing blocked_reason stays
--     as a human-readable summary.
--
-- No data migration needed. Existing rows have:
--   intake_source = 'admin' (DEFAULT)
--   status_token_hash = NULL (unused for admin-created)
--   requester_identity_verified_at = NULL (verification was Redis-side)

ALTER TABLE privacy_dsr_requests
    ADD COLUMN requester_identity_verified_at TIMESTAMPTZ,
    ADD COLUMN status_token_hash              BYTEA,
    ADD COLUMN intake_source                  TEXT NOT NULL DEFAULT 'admin'
        CHECK (intake_source IN ('admin', 'public_form')),
    -- Per ADR 0037 SLA monitor: track the last 12h-window warning
    -- emit so we don't spam compliance officers every hour. NULL =
    -- never warned for this request.
    ADD COLUMN last_sla_notified_at           TIMESTAMPTZ;

-- Lookup index for the public status endpoint. Partial because most
-- rows are admin-created with NULL token; the index stays small.
CREATE INDEX idx_dsr_status_token
    ON privacy_dsr_requests(tenant_id, status_token_hash)
    WHERE status_token_hash IS NOT NULL;

-- ---- artifacts -----------------------------------------------------
CREATE TABLE dsr_request_artifacts (
    tenant_id      UUID NOT NULL REFERENCES organizations(id),
    id             UUID NOT NULL DEFAULT gen_random_uuid(),
    request_id     UUID NOT NULL,
    artifact_type  TEXT NOT NULL CHECK (artifact_type IN
        ('export_zip', 'rectification_diff', 'erasure_report')),
    storage_bucket TEXT NOT NULL,
    storage_key    TEXT NOT NULL,
    size_bytes     BIGINT,
    sha256_hash    TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, request_id)
        REFERENCES privacy_dsr_requests(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_dsr_artifacts_request ON dsr_request_artifacts(tenant_id, request_id);

ALTER TABLE dsr_request_artifacts ENABLE ROW LEVEL SECURITY;
ALTER TABLE dsr_request_artifacts FORCE  ROW LEVEL SECURITY;
CREATE POLICY dsr_request_artifacts_iso ON dsr_request_artifacts
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY dsr_request_artifacts_iso_insert ON dsr_request_artifacts
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ---- conflicts -----------------------------------------------------
CREATE TABLE dsr_conflicts (
    tenant_id          UUID NOT NULL REFERENCES organizations(id),
    id                 UUID NOT NULL DEFAULT gen_random_uuid(),
    request_id         UUID NOT NULL,
    -- legal_hold       — at least one document referenced by the
    --                    subject is on hold. conflict_details has
    --                    {hold_id, matter_name, document_count}.
    -- retention_conflict — subject-owned doc is past retain_days but
    --                    hasn't gone through disposition. details has
    --                    {policy_id, document_count, oldest_doc_id}.
    -- multi_tenant     — subject email exists in another tenant; needs
    --                    explicit acknowledgement before this tenant's
    --                    erasure proceeds.
    conflict_type      TEXT NOT NULL CHECK (conflict_type IN
        ('legal_hold', 'retention_conflict', 'multi_tenant')),
    conflict_details   JSONB NOT NULL,
    resolved_by        UUID,
    resolved_at        TIMESTAMPTZ,
    resolution_action  TEXT, -- e.g. 'partial_erase', 'release_hold', 'reject_request'
    resolution_notes   TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, request_id)
        REFERENCES privacy_dsr_requests(tenant_id, id) ON DELETE CASCADE,
    -- Sanity: a resolved row needs both stamp columns; an unresolved
    -- row has neither.
    CHECK ((resolved_by IS NULL AND resolved_at IS NULL) OR
           (resolved_by IS NOT NULL AND resolved_at IS NOT NULL))
);
CREATE INDEX idx_dsr_conflicts_unresolved
    ON dsr_conflicts(tenant_id, request_id)
    WHERE resolved_at IS NULL;
CREATE INDEX idx_dsr_conflicts_request
    ON dsr_conflicts(tenant_id, request_id);

ALTER TABLE dsr_conflicts ENABLE ROW LEVEL SECURITY;
ALTER TABLE dsr_conflicts FORCE  ROW LEVEL SECURITY;
CREATE POLICY dsr_conflicts_iso ON dsr_conflicts
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY dsr_conflicts_iso_insert ON dsr_conflicts
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
