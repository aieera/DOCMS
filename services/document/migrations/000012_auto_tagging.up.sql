-- Auto-tagging — intelligence-generated tag suggestions with per-tenant
-- thresholds. The `documents.tags` TEXT[] column is the source of truth for
-- applied tags; this table only records the *suggestions* the intelligence
-- pipeline generates so admins can review and accept/reject them.

CREATE TABLE tag_suggestions (
    tenant_id     UUID        NOT NULL REFERENCES organizations(id),
    id            UUID        NOT NULL DEFAULT gen_random_uuid(),
    document_id   UUID        NOT NULL,
    version_id    UUID        NOT NULL,
    tag_name      TEXT        NOT NULL,            -- normalised lowercase
    source        TEXT        NOT NULL CHECK (source IN ('ner','classification','llm','pattern')),
    source_detail JSONB       NOT NULL DEFAULT '{}'::jsonb,
    confidence    REAL        NOT NULL CHECK (confidence BETWEEN 0.0 AND 1.0),
    status        TEXT        NOT NULL DEFAULT 'pending'
                              CHECK (status IN ('pending','accepted','rejected','auto_applied')),
    reviewed_by   UUID,
    reviewed_at   TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, document_id) REFERENCES documents (tenant_id, id),
    FOREIGN KEY (tenant_id, version_id)  REFERENCES versions  (tenant_id, id),
    FOREIGN KEY (tenant_id, reviewed_by) REFERENCES users     (tenant_id, id)
);

-- Per-document lookup (suggestion panel on the doc detail page).
CREATE INDEX idx_tag_suggestions_doc
    ON tag_suggestions (tenant_id, document_id, status);

-- Admin review queue — only pending rows, sorted by confidence.
CREATE INDEX idx_tag_suggestions_pending
    ON tag_suggestions (tenant_id, confidence DESC, created_at DESC)
    WHERE status = 'pending';

-- Suppress duplicate (doc, tag, source) suggestions from at-least-once
-- redelivery of the trigger event. Pending only — once reviewed, the
-- suggestion stays for audit even if intelligence re-suggests the
-- same tag from a later run.
CREATE UNIQUE INDEX uniq_tag_suggestions_pending_pair
    ON tag_suggestions (tenant_id, document_id, tag_name, source)
    WHERE status = 'pending';

ALTER TABLE tag_suggestions ENABLE ROW LEVEL SECURITY;
ALTER TABLE tag_suggestions FORCE  ROW LEVEL SECURITY;
CREATE POLICY tag_suggestions_tenant_isolation ON tag_suggestions
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY tag_suggestions_tenant_isolation_insert ON tag_suggestions
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ---------------------------------------------------------------------------
-- Per-tenant configuration. Single row per tenant; absence = use code defaults.
-- ---------------------------------------------------------------------------
CREATE TABLE auto_tag_config (
    tenant_id            UUID        NOT NULL PRIMARY KEY REFERENCES organizations(id),
    enabled              BOOLEAN     NOT NULL DEFAULT true,
    auto_apply_threshold REAL        NOT NULL DEFAULT 0.95
                                     CHECK (auto_apply_threshold BETWEEN 0.0 AND 1.0),
    suggest_threshold    REAL        NOT NULL DEFAULT 0.60
                                     CHECK (suggest_threshold BETWEEN 0.0 AND 1.0),
    max_tags_per_document INT        NOT NULL DEFAULT 20
                                     CHECK (max_tags_per_document BETWEEN 1 AND 100),
    blocked_tags         TEXT[]      NOT NULL DEFAULT '{}',
    source_weights       JSONB       NOT NULL DEFAULT
        '{"ner":1.0,"classification":0.8,"llm":0.9,"pattern":0.7}'::jsonb,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),

    CHECK (auto_apply_threshold >= suggest_threshold)
);

ALTER TABLE auto_tag_config ENABLE ROW LEVEL SECURITY;
ALTER TABLE auto_tag_config FORCE  ROW LEVEL SECURITY;
CREATE POLICY auto_tag_config_tenant_isolation ON auto_tag_config
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY auto_tag_config_tenant_isolation_insert ON auto_tag_config
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TRIGGER update_auto_tag_config_updated_at
    BEFORE UPDATE ON auto_tag_config
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
