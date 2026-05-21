-- ADR 0115 Phase 1 — document processing failure surface.
--
-- Two-part schema:
--   1. `documents.processing_status` rolls up per-stage outcomes into a
--      single value the UI can render at a glance.
--   2. `document_processing_stages` is the source of truth — one row per
--      (document, stage) with attempts, failure_reason, failure_detail.
--
-- Phase 1 instruments OCR only. Future phases extend to classify, NER,
-- embed, lang_detect, route_suggest, ocr_quality, virus_scan. The CHECK
-- constraints below pre-declare every stage so subsequent phases don't
-- need a CHECK migration.

-- Roll-up column. `partial` = some stages completed, some failed.
ALTER TABLE documents
    ADD COLUMN IF NOT EXISTS processing_status text NOT NULL DEFAULT 'pending';

ALTER TABLE documents
    DROP CONSTRAINT IF EXISTS documents_processing_status_check;

ALTER TABLE documents
    ADD CONSTRAINT documents_processing_status_check
    CHECK (processing_status IN
        ('pending','running','completed','failed','partial'));

-- Helps the workspace grid filter for "Processing failed" without
-- scanning the whole tenant.
CREATE INDEX IF NOT EXISTS idx_documents_processing_status_failed
    ON documents (tenant_id, processing_status)
    WHERE processing_status IN ('failed','partial');

-- Per-stage row. (tenant_id, document_id, stage) is the primary key —
-- only one logical state per (doc, stage); retries bump `attempts`.
-- version_id lets stages stay accurate across version replacements.
CREATE TABLE IF NOT EXISTS document_processing_stages (
    tenant_id        uuid        NOT NULL REFERENCES organizations(id),
    document_id      uuid        NOT NULL,
    version_id       uuid        NOT NULL,
    stage            text        NOT NULL,
    status           text        NOT NULL DEFAULT 'pending',
    attempts         integer     NOT NULL DEFAULT 0,
    -- Stable taxonomy code (see ADR 0115 § Failure-reason taxonomy).
    -- Worker maps caught exceptions to one of these via the
    -- error_classifier helper; never NULL when status='failed'.
    failure_reason   text,
    -- Human-readable detail, post-sanitization. Safe to display to
    -- admins. Raw exception messages go to structured logs only.
    failure_detail   text,
    started_at       timestamptz,
    completed_at     timestamptz,
    PRIMARY KEY (tenant_id, document_id, stage),
    FOREIGN KEY (tenant_id, document_id)
        REFERENCES documents (tenant_id, id)
        ON DELETE CASCADE,
    CONSTRAINT document_processing_stages_stage_check
        CHECK (stage IN
            ('upload','virus_scan','ocr','ocr_quality','classify',
             'ner','embed','lang_detect','route_suggest')),
    CONSTRAINT document_processing_stages_status_check
        CHECK (status IN
            ('pending','running','completed','failed','skipped')),
    -- A failed row must carry a reason code so dashboards never bucket
    -- to "unknown" by accident.
    CONSTRAINT document_processing_stages_failed_has_reason
        CHECK (status <> 'failed' OR failure_reason IS NOT NULL)
);

-- Look up all stages for one doc (UI). The PK already covers this
-- shape, but a covering index over the columns the API reads keeps
-- the doc-detail panel one round-trip.
CREATE INDEX IF NOT EXISTS idx_processing_stages_doc
    ON document_processing_stages (tenant_id, document_id, stage);

-- Operator dashboards: "every failed stage across the tenant in the
-- last 24h". WHERE-partial keeps the index small.
CREATE INDEX IF NOT EXISTS idx_processing_stages_failed_recent
    ON document_processing_stages (tenant_id, completed_at DESC)
    WHERE status = 'failed';

ALTER TABLE document_processing_stages ENABLE ROW LEVEL SECURITY;
ALTER TABLE document_processing_stages FORCE ROW LEVEL SECURITY;
CREATE POLICY document_processing_stages_tenant_isolation
    ON document_processing_stages
    USING (tenant_id::text = current_setting('app.current_tenant', true))
    WITH CHECK (tenant_id::text = current_setting('app.current_tenant', true));

COMMENT ON COLUMN documents.processing_status IS
    'ADR 0115 roll-up of per-stage outcomes in document_processing_stages. '
    'pending = no stage started; running = at least one stage in flight; '
    'completed = every applicable stage completed; failed = at least one '
    'stage failed and no recovery; partial = mix of completed + failed '
    'where the doc is still partly usable.';

COMMENT ON COLUMN document_processing_stages.failure_reason IS
    'Stable taxonomy code: unsupported_mime, file_corrupted, '
    'dependency_unavailable, dependency_quota, timeout, decrypt_failed, '
    'event_publish_failed, worker_crash, policy_denied, unknown. '
    'Maps 1:1 to localized UI copy + dashboard buckets.';
