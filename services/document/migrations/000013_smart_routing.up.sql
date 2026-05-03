-- Smart routing — admin-defined rules + learned patterns + per-doc suggestions.
-- ADR 0053 documents the strategy split (rule | history | similarity).

-- ---------------------------------------------------------------------------
-- routing_rules: admin-managed mapping classification.category_key → folder.
-- ---------------------------------------------------------------------------
CREATE TABLE routing_rules (
    tenant_id            UUID        NOT NULL REFERENCES organizations(id),
    id                   UUID        NOT NULL DEFAULT gen_random_uuid(),
    name                 TEXT        NOT NULL,
    description          TEXT,
    category_key         TEXT        NOT NULL,
    target_folder_id     UUID        NOT NULL,
    target_workspace_id  UUID,
    priority             INT         NOT NULL DEFAULT 0,
    enabled              BOOLEAN     NOT NULL DEFAULT true,
    created_by           UUID        NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, target_folder_id) REFERENCES folders (tenant_id, id),
    FOREIGN KEY (tenant_id, created_by)       REFERENCES users   (tenant_id, id),
    UNIQUE (tenant_id, category_key, target_folder_id)
);

CREATE INDEX idx_routing_rules_category
    ON routing_rules (tenant_id, category_key, priority DESC)
    WHERE enabled = true;

ALTER TABLE routing_rules ENABLE ROW LEVEL SECURITY;
ALTER TABLE routing_rules FORCE  ROW LEVEL SECURITY;
CREATE POLICY routing_rules_tenant_isolation ON routing_rules
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY routing_rules_tenant_isolation_insert ON routing_rules
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TRIGGER update_routing_rules_updated_at
    BEFORE UPDATE ON routing_rules
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- ---------------------------------------------------------------------------
-- filing_history: every successful "doc landed in folder" event, used by
-- the history strategy to learn user filing patterns. Recorded by the
-- document service inside the same tx as MoveDocument / CreateDocument.
-- ---------------------------------------------------------------------------
CREATE TABLE filing_history (
    tenant_id            UUID        NOT NULL REFERENCES organizations(id),
    id                   UUID        NOT NULL DEFAULT gen_random_uuid(),
    document_id          UUID        NOT NULL,
    category_key         TEXT,
    filed_folder_id      UUID        NOT NULL,
    filed_workspace_id   UUID,
    was_suggestion       BOOLEAN     NOT NULL DEFAULT false,
    suggestion_rank      INT,
    filed_by             UUID        NOT NULL,
    filed_at             TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, document_id)     REFERENCES documents (tenant_id, id),
    FOREIGN KEY (tenant_id, filed_folder_id) REFERENCES folders   (tenant_id, id),
    FOREIGN KEY (tenant_id, filed_by)        REFERENCES users     (tenant_id, id)
);

CREATE INDEX idx_filing_history_category
    ON filing_history (tenant_id, category_key, filed_folder_id);
CREATE INDEX idx_filing_history_user
    ON filing_history (tenant_id, filed_by, filed_at DESC);

ALTER TABLE filing_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE filing_history FORCE  ROW LEVEL SECURITY;
CREATE POLICY filing_history_tenant_isolation ON filing_history
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY filing_history_tenant_isolation_insert ON filing_history
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ---------------------------------------------------------------------------
-- route_suggestions: per-document folder suggestions written by the
-- intelligence smart_route task. Status is the user's review state.
-- ---------------------------------------------------------------------------
CREATE TABLE route_suggestions (
    tenant_id              UUID        NOT NULL REFERENCES organizations(id),
    id                     UUID        NOT NULL DEFAULT gen_random_uuid(),
    document_id            UUID        NOT NULL,
    version_id             UUID        NOT NULL,
    suggested_folder_id    UUID        NOT NULL,
    suggested_workspace_id UUID,
    folder_path            TEXT        NOT NULL,
    match_source           TEXT        NOT NULL CHECK (match_source IN ('rule','history','similarity')),
    match_detail           JSONB       NOT NULL DEFAULT '{}'::jsonb,
    confidence             REAL        NOT NULL CHECK (confidence BETWEEN 0.0 AND 1.0),
    status                 TEXT        NOT NULL DEFAULT 'pending'
                                       CHECK (status IN ('pending','accepted','dismissed')),
    accepted_by            UUID,
    accepted_at            TIMESTAMPTZ,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, document_id)         REFERENCES documents (tenant_id, id),
    FOREIGN KEY (tenant_id, version_id)          REFERENCES versions  (tenant_id, id),
    FOREIGN KEY (tenant_id, suggested_folder_id) REFERENCES folders   (tenant_id, id),
    FOREIGN KEY (tenant_id, accepted_by)         REFERENCES users     (tenant_id, id)
);

CREATE INDEX idx_route_suggestions_doc
    ON route_suggestions (tenant_id, document_id, status, confidence DESC);

-- Suppress duplicate (doc, folder, source) pending suggestions from at-
-- least-once redelivery. Once reviewed the row stays for audit and a
-- later run is allowed to re-suggest the same folder.
CREATE UNIQUE INDEX uniq_route_suggestions_pending_pair
    ON route_suggestions (tenant_id, document_id, suggested_folder_id, match_source)
    WHERE status = 'pending';

ALTER TABLE route_suggestions ENABLE ROW LEVEL SECURITY;
ALTER TABLE route_suggestions FORCE  ROW LEVEL SECURITY;
CREATE POLICY route_suggestions_tenant_isolation ON route_suggestions
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY route_suggestions_tenant_isolation_insert ON route_suggestions
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ---------------------------------------------------------------------------
-- Per-tenant config. CHECK enforces auto_move >= suggest so the table
-- can never represent inverted gating.
-- ---------------------------------------------------------------------------
CREATE TABLE smart_routing_config (
    tenant_id            UUID        NOT NULL PRIMARY KEY REFERENCES organizations(id),
    enabled              BOOLEAN     NOT NULL DEFAULT true,
    auto_move_threshold  REAL        NOT NULL DEFAULT 0.98
                                     CHECK (auto_move_threshold BETWEEN 0.0 AND 1.0),
    suggest_threshold    REAL        NOT NULL DEFAULT 0.50
                                     CHECK (suggest_threshold BETWEEN 0.0 AND 1.0),
    max_suggestions      INT         NOT NULL DEFAULT 5
                                     CHECK (max_suggestions BETWEEN 1 AND 20),
    learn_from_history   BOOLEAN     NOT NULL DEFAULT true,
    use_similarity       BOOLEAN     NOT NULL DEFAULT true,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),

    CHECK (auto_move_threshold >= suggest_threshold)
);

ALTER TABLE smart_routing_config ENABLE ROW LEVEL SECURITY;
ALTER TABLE smart_routing_config FORCE  ROW LEVEL SECURITY;
CREATE POLICY smart_routing_config_tenant_isolation ON smart_routing_config
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY smart_routing_config_tenant_isolation_insert ON smart_routing_config
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TRIGGER update_smart_routing_config_updated_at
    BEFORE UPDATE ON smart_routing_config
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
