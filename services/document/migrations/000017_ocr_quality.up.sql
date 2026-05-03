-- OCR quality scoring (ADR 0057) — per-page composite score plus
-- a per-document rollup, both refreshed on every OCR completion.

CREATE TABLE ocr_quality_scores (
    tenant_id        UUID        NOT NULL REFERENCES organizations(id),
    id               UUID        NOT NULL DEFAULT gen_random_uuid(),
    document_id      UUID        NOT NULL,
    version_id       UUID        NOT NULL,
    page_number      INT         NOT NULL CHECK (page_number > 0),
    overall_score    REAL        NOT NULL CHECK (overall_score BETWEEN 0.0 AND 1.0),
    char_confidence  REAL,
    word_density     REAL,
    line_regularity  REAL,
    noise_ratio      REAL,
    language_score   REAL,
    issues           TEXT[]      NOT NULL DEFAULT '{}'::text[],
    word_count       INT         NOT NULL DEFAULT 0,
    char_count       INT         NOT NULL DEFAULT 0,
    needs_review     BOOLEAN     NOT NULL DEFAULT false,
    reviewed         BOOLEAN     NOT NULL DEFAULT false,
    reviewed_by      UUID,
    reviewed_at      TIMESTAMPTZ,
    review_note      TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, document_id) REFERENCES documents (tenant_id, id),
    FOREIGN KEY (tenant_id, version_id)  REFERENCES document_versions  (tenant_id, id),
    FOREIGN KEY (tenant_id, reviewed_by) REFERENCES users     (tenant_id, id),
    UNIQUE (tenant_id, version_id, page_number)
);

CREATE INDEX idx_ocr_quality_doc
    ON ocr_quality_scores (tenant_id, document_id);
CREATE INDEX idx_ocr_quality_review
    ON ocr_quality_scores (tenant_id, overall_score, created_at DESC)
    WHERE needs_review = true AND reviewed = false;

ALTER TABLE ocr_quality_scores ENABLE ROW LEVEL SECURITY;
ALTER TABLE ocr_quality_scores FORCE  ROW LEVEL SECURITY;
CREATE POLICY ocr_quality_scores_tenant_isolation ON ocr_quality_scores
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY ocr_quality_scores_tenant_isolation_insert ON ocr_quality_scores
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TRIGGER update_ocr_quality_scores_updated_at
    BEFORE UPDATE ON ocr_quality_scores
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- ---------------------------------------------------------------------------
-- ocr_quality_summary — one row per document, UPSERT per scan.
-- ---------------------------------------------------------------------------
CREATE TABLE ocr_quality_summary (
    tenant_id            UUID        NOT NULL REFERENCES organizations(id),
    document_id          UUID        NOT NULL,
    version_id           UUID        NOT NULL,
    avg_score            REAL        NOT NULL CHECK (avg_score BETWEEN 0.0 AND 1.0),
    min_score            REAL        NOT NULL CHECK (min_score BETWEEN 0.0 AND 1.0),
    max_score            REAL        NOT NULL CHECK (max_score BETWEEN 0.0 AND 1.0),
    total_pages          INT         NOT NULL CHECK (total_pages >= 0),
    pages_needing_review INT         NOT NULL DEFAULT 0,
    quality_grade        TEXT        NOT NULL
                                     CHECK (quality_grade IN ('excellent','good','fair','poor')),
    auto_retried         BOOLEAN     NOT NULL DEFAULT false,
    scored_at            TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, document_id),
    FOREIGN KEY (tenant_id, document_id) REFERENCES documents (tenant_id, id),
    FOREIGN KEY (tenant_id, version_id)  REFERENCES document_versions  (tenant_id, id)
);

CREATE INDEX idx_ocr_quality_summary_grade
    ON ocr_quality_summary (tenant_id, quality_grade, scored_at DESC);
CREATE INDEX idx_ocr_quality_summary_review
    ON ocr_quality_summary (tenant_id, pages_needing_review)
    WHERE pages_needing_review > 0;

ALTER TABLE ocr_quality_summary ENABLE ROW LEVEL SECURITY;
ALTER TABLE ocr_quality_summary FORCE  ROW LEVEL SECURITY;
CREATE POLICY ocr_quality_summary_tenant_isolation ON ocr_quality_summary
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY ocr_quality_summary_tenant_isolation_insert ON ocr_quality_summary
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ---------------------------------------------------------------------------
-- ocr_quality_config — per-tenant thresholds.
-- ---------------------------------------------------------------------------
CREATE TABLE ocr_quality_config (
    tenant_id            UUID        NOT NULL PRIMARY KEY REFERENCES organizations(id),
    enabled              BOOLEAN     NOT NULL DEFAULT true,
    review_threshold     REAL        NOT NULL DEFAULT 0.60
                                     CHECK (review_threshold BETWEEN 0.0 AND 1.0),
    excellent_threshold  REAL        NOT NULL DEFAULT 0.90
                                     CHECK (excellent_threshold BETWEEN 0.0 AND 1.0),
    good_threshold       REAL        NOT NULL DEFAULT 0.75
                                     CHECK (good_threshold BETWEEN 0.0 AND 1.0),
    fair_threshold       REAL        NOT NULL DEFAULT 0.60
                                     CHECK (fair_threshold BETWEEN 0.0 AND 1.0),
    auto_retry_below     REAL        NOT NULL DEFAULT 0.40
                                     CHECK (auto_retry_below BETWEEN 0.0 AND 1.0),
    notify_on_poor       BOOLEAN     NOT NULL DEFAULT false,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Grade thresholds must be ordered.
    CHECK (excellent_threshold >= good_threshold),
    CHECK (good_threshold      >= fair_threshold)
);

ALTER TABLE ocr_quality_config ENABLE ROW LEVEL SECURITY;
ALTER TABLE ocr_quality_config FORCE  ROW LEVEL SECURITY;
CREATE POLICY ocr_quality_config_tenant_isolation ON ocr_quality_config
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY ocr_quality_config_tenant_isolation_insert ON ocr_quality_config
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TRIGGER update_ocr_quality_config_updated_at
    BEFORE UPDATE ON ocr_quality_config
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
