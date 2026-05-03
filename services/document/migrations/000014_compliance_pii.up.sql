-- Compliance scanning — PII/PHI detection from NER + regex (ADR 0054).
--
-- Two persistence shapes:
--   compliance_findings   one row per (document, version, entity_type)
--                         capturing count, pages, sample, encrypted values
--   compliance_summary    one row per document (overwritten per scan) with
--                         a precomputed risk rollup so list views avoid
--                         scanning every finding.
-- Plus per-tenant compliance_config.

CREATE TABLE compliance_findings (
    tenant_id          UUID        NOT NULL REFERENCES organizations(id),
    id                 UUID        NOT NULL DEFAULT gen_random_uuid(),
    document_id        UUID        NOT NULL,
    version_id         UUID        NOT NULL,
    entity_type        TEXT        NOT NULL,
    entity_category    TEXT        NOT NULL CHECK (entity_category IN ('pii','phi')),
    occurrence_count   INT         NOT NULL CHECK (occurrence_count >= 0),
    page_numbers       INT[]       NOT NULL DEFAULT '{}',
    confidence         REAL        NOT NULL CHECK (confidence BETWEEN 0.0 AND 1.0),
    risk_level         TEXT        NOT NULL CHECK (risk_level IN ('critical','high','medium','low')),
    sample_context     TEXT,
    encrypted_values   BYTEA,
    detection_source   TEXT        NOT NULL DEFAULT 'ner'
                                   CHECK (detection_source IN ('ner','pattern','custom')),
    remediation_status TEXT        NOT NULL DEFAULT 'open'
                                   CHECK (remediation_status IN ('open','acknowledged','remediated','false_positive')),
    remediated_by      UUID,
    remediated_at      TIMESTAMPTZ,
    remediation_note   TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, document_id) REFERENCES documents (tenant_id, id),
    FOREIGN KEY (tenant_id, version_id)  REFERENCES document_versions  (tenant_id, id),
    FOREIGN KEY (tenant_id, remediated_by) REFERENCES users   (tenant_id, id)
);

CREATE INDEX idx_compliance_findings_doc
    ON compliance_findings (tenant_id, document_id);
CREATE INDEX idx_compliance_findings_risk
    ON compliance_findings (tenant_id, risk_level, remediation_status);
CREATE INDEX idx_compliance_findings_open
    ON compliance_findings (tenant_id, created_at DESC)
    WHERE remediation_status = 'open';

-- One pending finding per (doc, version, entity_type, source) — collapses
-- at-least-once redelivery. Once reviewed the row stays for audit.
CREATE UNIQUE INDEX uniq_compliance_findings_open_pair
    ON compliance_findings (tenant_id, version_id, entity_type, detection_source)
    WHERE remediation_status = 'open';

ALTER TABLE compliance_findings ENABLE ROW LEVEL SECURITY;
ALTER TABLE compliance_findings FORCE  ROW LEVEL SECURITY;
CREATE POLICY compliance_findings_tenant_isolation ON compliance_findings
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY compliance_findings_tenant_isolation_insert ON compliance_findings
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ---------------------------------------------------------------------------
-- compliance_summary — denormalised rollup, one row per document. Refreshed
-- on every scan (UPSERT). Drives the doc list compliance badge.
-- ---------------------------------------------------------------------------
CREATE TABLE compliance_summary (
    tenant_id          UUID        NOT NULL REFERENCES organizations(id),
    document_id        UUID        NOT NULL,
    version_id         UUID        NOT NULL,
    overall_risk       TEXT        NOT NULL DEFAULT 'none'
                                   CHECK (overall_risk IN ('none','low','medium','high','critical')),
    pii_count          INT         NOT NULL DEFAULT 0,
    phi_count          INT         NOT NULL DEFAULT 0,
    critical_count     INT         NOT NULL DEFAULT 0,
    high_count         INT         NOT NULL DEFAULT 0,
    medium_count       INT         NOT NULL DEFAULT 0,
    low_count          INT         NOT NULL DEFAULT 0,
    entity_types_found TEXT[]      NOT NULL DEFAULT '{}',
    needs_review       BOOLEAN     NOT NULL DEFAULT false,
    auto_held          BOOLEAN     NOT NULL DEFAULT false,
    scanned_at         TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, document_id),
    FOREIGN KEY (tenant_id, document_id) REFERENCES documents (tenant_id, id),
    FOREIGN KEY (tenant_id, version_id)  REFERENCES document_versions  (tenant_id, id)
);

CREATE INDEX idx_compliance_summary_risk
    ON compliance_summary (tenant_id, overall_risk, scanned_at DESC);
CREATE INDEX idx_compliance_summary_review
    ON compliance_summary (tenant_id, needs_review, scanned_at DESC)
    WHERE needs_review = true;

ALTER TABLE compliance_summary ENABLE ROW LEVEL SECURITY;
ALTER TABLE compliance_summary FORCE  ROW LEVEL SECURITY;
CREATE POLICY compliance_summary_tenant_isolation ON compliance_summary
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY compliance_summary_tenant_isolation_insert ON compliance_summary
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ---------------------------------------------------------------------------
-- Per-tenant config.
-- ---------------------------------------------------------------------------
CREATE TABLE compliance_config (
    tenant_id                  UUID        NOT NULL PRIMARY KEY REFERENCES organizations(id),
    enabled                    BOOLEAN     NOT NULL DEFAULT true,
    auto_hold_on_critical      BOOLEAN     NOT NULL DEFAULT false,
    notify_on_high             BOOLEAN     NOT NULL DEFAULT true,
    notify_roles               TEXT[]      NOT NULL DEFAULT '{compliance_officer,admin}',
    pii_entity_risk_overrides  JSONB       NOT NULL DEFAULT '{}'::jsonb,
    phi_enabled                BOOLEAN     NOT NULL DEFAULT false,
    custom_patterns            JSONB       NOT NULL DEFAULT '[]'::jsonb,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE compliance_config ENABLE ROW LEVEL SECURITY;
ALTER TABLE compliance_config FORCE  ROW LEVEL SECURITY;
CREATE POLICY compliance_config_tenant_isolation ON compliance_config
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY compliance_config_tenant_isolation_insert ON compliance_config
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TRIGGER update_compliance_config_updated_at
    BEFORE UPDATE ON compliance_config
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
