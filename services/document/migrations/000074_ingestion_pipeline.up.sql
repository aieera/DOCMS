-- Pre-commit ingestion pipeline (Workstream 3 — "fix OCR ordering").
--
-- Today OCR runs AFTER a version exists (the intelligence worker consumes
-- dms.version.uploaded.v1), so the system can't read a page before deciding
-- new-document-vs-new-version. This migration introduces a staging-then-route
-- pipeline that sits in FRONT of document/version creation:
--
--   ingestion_items   — one staged blob awaiting OCR + routing. NO document or
--                       version exists yet; routing (a Temporal workflow)
--                       commits it as a new version of a matched document, a
--                       brand-new document, or sends it to review.
--   ingestion_ocr     — sidecar holding the OCR full text for a staged item
--                       (ocr_result_ref points here). Distinct from ocr_results,
--                       which is keyed by version_id — a staged item has none.
--   review_queue_items — low-confidence / ambiguous reads land here for a human
--                       to resolve. Never auto-versioned.
--
-- Document-service-owned (RLS + the routing/commit path live with the rest of
-- the document schema) but ingestion_items + ingestion_ocr are written by the
-- intelligence worker over the shared Postgres — the same split already used
-- for ocr_results / extracted_fields.

-- One row per staged blob. blob_ref is a content_blobs.id (the bytes are
-- already uploaded via the storage initiate/PUT/complete flow and dedup by
-- sha256); storage_bucket/storage_key are denormalized off content_blobs so the
-- intelligence worker can fetch the bytes without a document-service round-trip.
CREATE TABLE ingestion_items (
    tenant_id             UUID NOT NULL REFERENCES organizations(id),
    id                    UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id          UUID NOT NULL,
    -- folder the routed document lands in (new-doc path needs a folder).
    folder_id             UUID,
    -- caller-supplied routing hint (e.g. an ERP customer id); part of the
    -- ingest-time idempotency key so re-POSTing the same blob for the same
    -- customer converges on one staging row.
    target_customer_ref   TEXT NOT NULL DEFAULT '',
    blob_ref              UUID NOT NULL,             -- content_blobs.id
    blob_checksum         TEXT NOT NULL,             -- sha256 hex
    status                TEXT NOT NULL DEFAULT 'received'
        CHECK (status IN ('received','ocr_running','processed','routed',
                          'needs_review','committed','rejected')),
    -- OCR + extraction results, written back by the intelligence worker.
    ocr_result_ref        UUID,                      -- → ingestion_ocr.id
    extracted_external_key TEXT NOT NULL DEFAULT '',
    match_document_id      UUID,                      -- set by the route step
    confidence             DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    -- routing inputs / S3 fetch coordinates.
    document_class        TEXT NOT NULL DEFAULT '',
    storage_bucket        TEXT NOT NULL DEFAULT '',
    storage_key           TEXT NOT NULL DEFAULT '',
    mime_type             TEXT NOT NULL DEFAULT '',
    region_pin            TEXT NOT NULL DEFAULT '',
    -- principal that POSTed the blob; the route step acts as this user so the
    -- committed document/version carry a real created_by FK.
    created_by            UUID,
    failure_reason        TEXT NOT NULL DEFAULT '',
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, match_document_id) REFERENCES documents(tenant_id, id)
);
-- Queue ordering + the needs_review / status listings the REST surface serves.
CREATE INDEX idx_ingestion_items_status ON ingestion_items(tenant_id, status, created_at);
-- Ingest-time idempotency: at most one ACTIVE (not-yet-terminal) staging row
-- per (tenant, checksum, target). A re-POST of the same blob converges here via
-- INSERT ... ON CONFLICT DO NOTHING. Terminal rows are excluded so the same
-- blob can be re-ingested later (e.g. a corrected re-send).
CREATE UNIQUE INDEX idx_ingestion_items_dedup
    ON ingestion_items(tenant_id, blob_checksum, target_customer_ref)
    WHERE status NOT IN ('committed','rejected');
ALTER TABLE ingestion_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE ingestion_items FORCE  ROW LEVEL SECURITY;
CREATE POLICY ingestion_items_tenant_isolation ON ingestion_items
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY ingestion_items_tenant_isolation_insert ON ingestion_items
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- OCR sidecar for a staged item. One row per ingestion_item (the staged blob
-- is a single logical document); full_text is the concatenated page text the
-- route step's key extraction and the review UI read.
CREATE TABLE ingestion_ocr (
    tenant_id         UUID NOT NULL REFERENCES organizations(id),
    id                UUID NOT NULL DEFAULT gen_random_uuid(),
    ingestion_item_id UUID NOT NULL,
    full_text         TEXT NOT NULL DEFAULT '',
    page_count        INT  NOT NULL DEFAULT 0,
    confidence_avg    DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    engine            TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    -- One OCR result per staged item; the worker upserts on re-delivery.
    UNIQUE (tenant_id, ingestion_item_id),
    FOREIGN KEY (tenant_id, ingestion_item_id) REFERENCES ingestion_items(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE ingestion_ocr ENABLE ROW LEVEL SECURITY;
ALTER TABLE ingestion_ocr FORCE  ROW LEVEL SECURITY;
CREATE POLICY ingestion_ocr_tenant_isolation ON ingestion_ocr
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY ingestion_ocr_tenant_isolation_insert ON ingestion_ocr
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- Human review queue for low-confidence / ambiguous staged reads. Created by
-- the route step's needs_review branch; resolved by a reviewer who either
-- commits (as a new version of a chosen document, or as a new document) or
-- rejects. candidate_document_id is the near-match the route step found, if any.
CREATE TABLE review_queue_items (
    tenant_id             UUID NOT NULL REFERENCES organizations(id),
    id                    UUID NOT NULL DEFAULT gen_random_uuid(),
    ingestion_item_id     UUID NOT NULL,
    workspace_id          UUID NOT NULL,
    target_customer_ref   TEXT NOT NULL DEFAULT '',
    document_class        TEXT NOT NULL DEFAULT '',
    extracted_external_key TEXT NOT NULL DEFAULT '',
    candidate_document_id UUID,
    confidence            DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    reason                TEXT NOT NULL DEFAULT 'below_threshold'
        CHECK (reason IN ('below_threshold','ambiguous_match',
                          'no_external_key','low_ocr_confidence')),
    status                TEXT NOT NULL DEFAULT 'open'
        CHECK (status IN ('open','resolved','rejected')),
    blob_ref              UUID NOT NULL,
    blob_checksum         TEXT NOT NULL,
    ocr_result_ref        UUID,
    notes                 TEXT NOT NULL DEFAULT '',
    resolution            TEXT
        CHECK (resolution IS NULL OR resolution IN
            ('committed_version','committed_new','rejected')),
    resulting_document_id UUID,
    resulting_version_id  UUID,
    resolved_by           UUID,
    resolved_at           TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    -- One open review per staged item; the route step is idempotent on re-run.
    UNIQUE (tenant_id, ingestion_item_id),
    FOREIGN KEY (tenant_id, ingestion_item_id) REFERENCES ingestion_items(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_review_queue_status ON review_queue_items(tenant_id, status, created_at);
ALTER TABLE review_queue_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE review_queue_items FORCE  ROW LEVEL SECURITY;
CREATE POLICY review_queue_items_tenant_isolation ON review_queue_items
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY review_queue_items_tenant_isolation_insert ON review_queue_items
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
