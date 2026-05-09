-- ADR 0079 — redaction review queue + applied-job ledger.
--
-- Two new tables, deliberately disjoint from the existing
-- document_redactions table (migration 8) which captures a different
-- model: one row per "admin draws regions + hits the legal-hold
-- redact button". That path stays in place; the new candidate-review
-- workflow described in blueprint §6.7 lives here.

-- ---- redaction_candidates -----------------------------------------------
-- One row per PII span the NER pipeline detected (or an admin manually
-- added). Reviewers approve / reject each candidate before any pixel is
-- burned. The pending → approved → applied (or rejected) lifecycle is
-- enforced by the service-layer state machine.
CREATE TABLE redaction_candidates (
    tenant_id        UUID        NOT NULL REFERENCES organizations(id),
    id               UUID        NOT NULL DEFAULT gen_random_uuid(),
    document_id      UUID        NOT NULL,
    version_id       UUID        NOT NULL,
    -- Source of the candidate (which detector flagged it). Mirrors
    -- document_entities.source so we can prefer regex hits over LLM
    -- hits when the same span is flagged twice.
    source           TEXT        NOT NULL DEFAULT 'ner'
                                 CHECK (source IN ('ner','manual','llm')),
    -- The original entity row that produced this candidate (nullable
    -- for manual additions).
    entity_id        UUID,
    entity_type      TEXT        NOT NULL,
    entity_value     TEXT        NOT NULL,
    -- Page-relative coordinates picked from ocr_results.word_boxes
    -- when available. Stored as a JSONB array of rectangles so a
    -- multi-line span (rare; matches our union-of-overlapping-words
    -- algorithm) can become several rectangles in one candidate.
    --   [{"page": 0, "x0": 72.0, "y0": 134.5, "x1": 102.7, "y1": 148.2}, ...]
    rectangles       JSONB       NOT NULL DEFAULT '[]'::jsonb,
    -- Page-relative character offsets, kept alongside rectangles so
    -- post-redaction OCR can verify the value is no longer present.
    page_number      INTEGER,
    char_start       INTEGER,
    char_end         INTEGER,
    status           TEXT        NOT NULL DEFAULT 'pending'
                                 CHECK (status IN ('pending','approved','rejected','applied')),
    reviewed_by      UUID,
    reviewed_at      TIMESTAMPTZ,
    review_note      TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, document_id) REFERENCES documents         (tenant_id, id),
    FOREIGN KEY (tenant_id, version_id)  REFERENCES document_versions (tenant_id, id),
    FOREIGN KEY (tenant_id, reviewed_by) REFERENCES users             (tenant_id, id)
);

-- Common access patterns:
--   "show me the review queue for this version, oldest first"
CREATE INDEX idx_redaction_candidates_version
    ON redaction_candidates (tenant_id, version_id, status, created_at);
--   "show me pending candidates only" — partial index narrows the scan
--   on the busy review-queue page.
CREATE INDEX idx_redaction_candidates_pending
    ON redaction_candidates (tenant_id, version_id)
    WHERE status = 'pending';
--   "what was approved for this doc" — used by the apply step.
CREATE INDEX idx_redaction_candidates_doc_status
    ON redaction_candidates (tenant_id, document_id, status);

ALTER TABLE redaction_candidates ENABLE ROW LEVEL SECURITY;
ALTER TABLE redaction_candidates FORCE  ROW LEVEL SECURITY;
CREATE POLICY redaction_candidates_tenant_isolation ON redaction_candidates
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY redaction_candidates_tenant_isolation_insert ON redaction_candidates
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);


-- ---- redaction_jobs -----------------------------------------------------
-- One row per "apply-all" run. Snapshots the candidate IDs that were
-- approved at apply-time so a later audit can see exactly what was
-- burned. Source / redacted version pair establishes the linkage —
-- the redacted version is a real document_versions row whose blob
-- holds the burned-in PDF; the source row stays untouched.
CREATE TABLE redaction_jobs (
    tenant_id            UUID        NOT NULL REFERENCES organizations(id),
    id                   UUID        NOT NULL DEFAULT gen_random_uuid(),
    document_id          UUID        NOT NULL,
    -- Version we read PII candidates from + the version we burned to
    -- replace it. The redacted version becomes the document's current
    -- version; the source is preserved with restricted access.
    source_version_id    UUID        NOT NULL,
    redacted_version_id  UUID,        -- nullable while the burn-in job is in flight
    status               TEXT        NOT NULL DEFAULT 'queued'
                                     CHECK (status IN ('queued','running','completed','failed')),
    -- Snapshot of which candidates were approved at apply-time. Stored
    -- as a JSONB array of {candidate_id, entity_type, entity_value,
    -- rectangles} so the row is self-contained for audit even if the
    -- candidate rows are later purged.
    candidates_snapshot  JSONB       NOT NULL DEFAULT '[]'::jsonb,
    candidate_count      INTEGER     NOT NULL DEFAULT 0,
    error_message        TEXT,
    applied_by           UUID        NOT NULL,
    applied_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at         TIMESTAMPTZ,

    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, document_id)         REFERENCES documents         (tenant_id, id),
    FOREIGN KEY (tenant_id, source_version_id)   REFERENCES document_versions (tenant_id, id),
    FOREIGN KEY (tenant_id, redacted_version_id) REFERENCES document_versions (tenant_id, id),
    FOREIGN KEY (tenant_id, applied_by)          REFERENCES users             (tenant_id, id)
);

CREATE INDEX idx_redaction_jobs_doc
    ON redaction_jobs (tenant_id, document_id, applied_at DESC);
-- "what's the original of this redacted version?" — used by the
-- gated /unredacted download endpoint.
CREATE INDEX idx_redaction_jobs_redacted_version
    ON redaction_jobs (tenant_id, redacted_version_id)
    WHERE redacted_version_id IS NOT NULL;

ALTER TABLE redaction_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE redaction_jobs FORCE  ROW LEVEL SECURITY;
CREATE POLICY redaction_jobs_tenant_isolation ON redaction_jobs
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY redaction_jobs_tenant_isolation_insert ON redaction_jobs
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
