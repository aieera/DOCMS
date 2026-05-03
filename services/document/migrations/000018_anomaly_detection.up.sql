-- Anomaly detection (ADR 0058) — workspace-level outlier analysis.
-- Reports are per-run; findings hang off a report. A separate per-tenant
-- config controls schedule + thresholds.

CREATE TABLE anomaly_reports (
    tenant_id        UUID        NOT NULL REFERENCES organizations(id),
    id               UUID        NOT NULL DEFAULT gen_random_uuid(),
    workspace_id     UUID,                              -- NULL = tenant-wide scan
    analysis_type    TEXT        NOT NULL CHECK (analysis_type IN
                                  ('metadata','content','behavioral','combined')),
    status           TEXT        NOT NULL DEFAULT 'pending'
                                 CHECK (status IN ('pending','processing','completed','failed')),
    total_documents  INT         NOT NULL DEFAULT 0,
    anomalies_found  INT         NOT NULL DEFAULT 0,
    summary          JSONB       NOT NULL DEFAULT '{}'::jsonb,
    error_message    TEXT,
    triggered_by     TEXT        NOT NULL DEFAULT 'manual'
                                 CHECK (triggered_by IN ('scheduled','manual','upload')),
    requested_by     UUID,
    completed_at     TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, workspace_id) REFERENCES workspaces (tenant_id, id),
    FOREIGN KEY (tenant_id, requested_by) REFERENCES users      (tenant_id, id)
);

CREATE INDEX idx_anomaly_reports_workspace
    ON anomaly_reports (tenant_id, workspace_id, created_at DESC);
CREATE INDEX idx_anomaly_reports_status
    ON anomaly_reports (tenant_id, status, created_at DESC)
    WHERE status IN ('pending','processing');

ALTER TABLE anomaly_reports ENABLE ROW LEVEL SECURITY;
ALTER TABLE anomaly_reports FORCE  ROW LEVEL SECURITY;
CREATE POLICY anomaly_reports_tenant_isolation ON anomaly_reports
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY anomaly_reports_tenant_isolation_insert ON anomaly_reports
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ---------------------------------------------------------------------------
-- anomaly_findings — one row per (report, document, anomaly_type).
-- The same document can appear in a report under multiple anomaly types
-- (e.g. size_outlier AND content_outlier).
-- ---------------------------------------------------------------------------
CREATE TABLE anomaly_findings (
    tenant_id        UUID        NOT NULL REFERENCES organizations(id),
    id               UUID        NOT NULL DEFAULT gen_random_uuid(),
    report_id        UUID        NOT NULL,
    document_id      UUID        NOT NULL,
    anomaly_type     TEXT        NOT NULL CHECK (anomaly_type IN
                                  ('size_outlier','content_outlier','misclassified',
                                   'wrong_workspace','unusual_upload_time',
                                   'rapid_uploads','duplicate_suspect')),
    severity         TEXT        NOT NULL CHECK (severity IN ('high','medium','low')),
    description      TEXT        NOT NULL,
    evidence         JSONB       NOT NULL DEFAULT '{}'::jsonb,
    z_score          REAL,
    similarity_score REAL,
    status           TEXT        NOT NULL DEFAULT 'open'
                                 CHECK (status IN ('open','acknowledged','resolved','false_positive')),
    resolved_by      UUID,
    resolved_at      TIMESTAMPTZ,
    resolution_note  TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, report_id)   REFERENCES anomaly_reports (tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, document_id) REFERENCES documents       (tenant_id, id),
    FOREIGN KEY (tenant_id, resolved_by) REFERENCES users           (tenant_id, id),
    UNIQUE (tenant_id, report_id, document_id, anomaly_type)
);

CREATE INDEX idx_anomaly_findings_report
    ON anomaly_findings (tenant_id, report_id);
CREATE INDEX idx_anomaly_findings_doc
    ON anomaly_findings (tenant_id, document_id);
CREATE INDEX idx_anomaly_findings_open
    ON anomaly_findings (tenant_id, severity, created_at DESC)
    WHERE status = 'open';

ALTER TABLE anomaly_findings ENABLE ROW LEVEL SECURITY;
ALTER TABLE anomaly_findings FORCE  ROW LEVEL SECURITY;
CREATE POLICY anomaly_findings_tenant_isolation ON anomaly_findings
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY anomaly_findings_tenant_isolation_insert ON anomaly_findings
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ---------------------------------------------------------------------------
-- Per-tenant config.
-- ---------------------------------------------------------------------------
CREATE TABLE anomaly_config (
    tenant_id                  UUID        NOT NULL PRIMARY KEY REFERENCES organizations(id),
    enabled                    BOOLEAN     NOT NULL DEFAULT true,
    schedule_cron              TEXT        NOT NULL DEFAULT '0 2 * * 0',
    z_score_threshold          REAL        NOT NULL DEFAULT 2.5
                                          CHECK (z_score_threshold > 0),
    content_distance_threshold REAL        NOT NULL DEFAULT 0.7
                                          CHECK (content_distance_threshold BETWEEN 0.0 AND 2.0),
    min_documents_for_analysis INT         NOT NULL DEFAULT 20
                                          CHECK (min_documents_for_analysis >= 5),
    analyze_metadata           BOOLEAN     NOT NULL DEFAULT true,
    analyze_content            BOOLEAN     NOT NULL DEFAULT true,
    analyze_behavioral         BOOLEAN     NOT NULL DEFAULT true,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE anomaly_config ENABLE ROW LEVEL SECURITY;
ALTER TABLE anomaly_config FORCE  ROW LEVEL SECURITY;
CREATE POLICY anomaly_config_tenant_isolation ON anomaly_config
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY anomaly_config_tenant_isolation_insert ON anomaly_config
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TRIGGER update_anomaly_config_updated_at
    BEFORE UPDATE ON anomaly_config
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
