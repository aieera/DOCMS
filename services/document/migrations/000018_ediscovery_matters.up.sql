-- Migration 000018 — eDiscovery matters + custodians + exports (ADR 0038).
--
-- Augments the Wave 8.5 / G9 ediscovery export flow. The existing
-- handler at services/document/internal/handler/ediscovery_handler.go
-- streams a ZIP atomically per call; this migration adds the
-- persistent matter / chain-of-custody substrate so two exports for
-- the same matter share an audit row, and a regulator's "prove this
-- 90-day-old bundle hasn't been tampered with" question has a
-- queryable answer.
--
-- Numbering note: this branch (ediscovery-foundation) is off main.
-- A sibling branch (admin-security-posture) ships 000016 and 000017.
-- Numbered 000018 to leave room for those to land first; if
-- ediscovery-foundation merges first, the sibling renames its
-- migrations on rebase.

CREATE TABLE ediscovery_matters (
    tenant_id      UUID NOT NULL REFERENCES organizations(id),
    id             UUID NOT NULL DEFAULT gen_random_uuid(),
    -- Operator-assigned matter number (e.g. "2026-PAT-014"). Unique
    -- per tenant; the natural human-friendly key on the
    -- /admin/ediscovery surface.
    matter_number  TEXT NOT NULL,
    name           TEXT NOT NULL,
    description    TEXT,
    created_by     UUID NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at      TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, matter_number)
);
CREATE INDEX idx_ediscovery_matters_open
    ON ediscovery_matters(tenant_id, created_at DESC)
    WHERE closed_at IS NULL;

ALTER TABLE ediscovery_matters ENABLE ROW LEVEL SECURITY;
ALTER TABLE ediscovery_matters FORCE  ROW LEVEL SECURITY;
CREATE POLICY ediscovery_matters_iso ON ediscovery_matters
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY ediscovery_matters_iso_insert ON ediscovery_matters
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ---- Custodians ----------------------------------------------------
-- A custodian is a tenant user whose documents/comments/etc. are in
-- scope for a matter. Persistent so the matter detail page can show
-- the list and the export wizard can default-include them.
CREATE TABLE ediscovery_custodians (
    tenant_id  UUID NOT NULL REFERENCES organizations(id),
    matter_id  UUID NOT NULL,
    user_id    UUID NOT NULL,
    added_by   UUID NOT NULL,
    added_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, matter_id, user_id),
    FOREIGN KEY (tenant_id, matter_id) REFERENCES ediscovery_matters(tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, user_id)   REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_ediscovery_custodians_user
    ON ediscovery_custodians(tenant_id, user_id);

ALTER TABLE ediscovery_custodians ENABLE ROW LEVEL SECURITY;
ALTER TABLE ediscovery_custodians FORCE  ROW LEVEL SECURITY;
CREATE POLICY ediscovery_custodians_iso ON ediscovery_custodians
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY ediscovery_custodians_iso_insert ON ediscovery_custodians
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ---- Exports -------------------------------------------------------
-- One row per export attempt. The existing synchronous handler always
-- writes status='completed' (it streams the ZIP back atomically and
-- by the time the row exists, the bundle has been emitted). The
-- async workflow follow-up will write 'pending' first and progress
-- through 'running' → 'completed'/'failed'.
CREATE TABLE ediscovery_exports (
    tenant_id         UUID NOT NULL REFERENCES organizations(id),
    id                UUID NOT NULL DEFAULT gen_random_uuid(),
    matter_id         UUID NOT NULL,
    requested_by      UUID NOT NULL,
    -- Resolved scope: documents/folders/search-query JSON. Persisted
    -- so reruns + audits can replay the same set even if the source
    -- (e.g. a saved search) has since been edited.
    scope_json        JSONB NOT NULL,
    status            TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'running', 'completed', 'failed')),
    started_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at      TIMESTAMPTZ,
    -- SHA-256 (hex) of the canonical-JSON manifest the bundle carries.
    -- The verify-on-demand endpoint recomputes it against the bundle's
    -- manifest.json to detect tamper.
    manifest_sha256   TEXT,
    -- S3 location of the bundle. NULL today (existing flow streams
    -- ZIP back to the caller without persisting); reserved for the
    -- async upload follow-up that lands a 30-day presigned URL.
    bundle_bucket     TEXT,
    bundle_key        TEXT,
    bundle_size_bytes BIGINT,
    error_summary     TEXT,
    note              TEXT,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, matter_id) REFERENCES ediscovery_matters(tenant_id, id),
    -- Sanity: completed/failed rows have completed_at; everything
    -- else has it NULL. Cheap consistency check.
    CHECK ((status IN ('pending', 'running')) = (completed_at IS NULL))
);
CREATE INDEX idx_ediscovery_exports_matter
    ON ediscovery_exports(tenant_id, matter_id, started_at DESC);
CREATE INDEX idx_ediscovery_exports_recent
    ON ediscovery_exports(tenant_id, started_at DESC);

ALTER TABLE ediscovery_exports ENABLE ROW LEVEL SECURITY;
ALTER TABLE ediscovery_exports FORCE  ROW LEVEL SECURITY;
CREATE POLICY ediscovery_exports_iso ON ediscovery_exports
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY ediscovery_exports_iso_insert ON ediscovery_exports
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
