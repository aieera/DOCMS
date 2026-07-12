-- Wave 5 Prompt 5.4: classification + NER persistence and dedupe.
--
-- * document_classifications captures the top pick per version. One
--   row per (tenant, version) — re-classification overwrites via
--   PRIMARY KEY conflict. Top-3 alternatives live in top3 JSONB so
--   consumers can weigh the decision without an extra join.
-- * document_entities is append-per-run: re-extraction is expected
--   to DELETE+INSERT within the same tenant-scoped tx to stay
--   idempotent across retries.
-- * intel_processed_events is a shared dedupe ledger across the
--   classify/ner/embed consumers, keyed by (tenant_id, consumer,
--   event_id). 14-day GC same as ocr_processed_events.
--
-- Index plan:
--   document_classifications: PK (tenant_id, version_id); lookup by
--       (tenant_id, document_id) + secondary by category_key.
--   document_entities: (tenant_id, version_id, entity_type) for
--       per-type counts; (tenant_id, version_id) for whole-doc scans.
--   intel_processed_events: PK + (processed_at) for GC.
BEGIN;

CREATE TABLE document_classifications (
    tenant_id       UUID        NOT NULL REFERENCES organizations(id),
    version_id      UUID        NOT NULL,
    document_id     UUID        NOT NULL,
    category_key    TEXT        NOT NULL,
    confidence      REAL        NOT NULL CHECK (confidence BETWEEN 0 AND 1),
    method          TEXT        NOT NULL CHECK (method IN ('rules', 'ml', 'llm')),
    model_version   TEXT        NOT NULL DEFAULT '',
    top3            JSONB       NOT NULL DEFAULT '[]'::jsonb,
    classified_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, version_id)
);

CREATE INDEX idx_document_classifications_doc
    ON document_classifications (tenant_id, document_id);
CREATE INDEX idx_document_classifications_category
    ON document_classifications (tenant_id, category_key);

ALTER TABLE document_classifications ENABLE ROW LEVEL SECURITY;
CREATE POLICY document_classifications_tenant_isolation
    ON document_classifications
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);


-- OWNERSHIP (ADR 0121, 2026-07-05): the DOCUMENT schema owns this base
-- table now — document 000021 carries an identical guarded definition,
-- because document reads the table (ner_repo.go) and evolves its
-- taxonomy there. This side is IF NOT EXISTS so either application
-- order works on a shared database. Editing this already-applied file
-- is safe: golang-migrate tracks version numbers, not checksums, and
-- environments that already ran it never re-run it. Keep the definition
-- column-for-column identical to document 000021 — the ordering test
-- (pkg/database/migration_ordering_integration_test.go) pins both orders.
CREATE TABLE IF NOT EXISTS document_entities (
    tenant_id      UUID        NOT NULL REFERENCES organizations(id),
    id             UUID        NOT NULL DEFAULT gen_random_uuid(),
    version_id     UUID        NOT NULL,
    document_id    UUID        NOT NULL,
    entity_type    TEXT        NOT NULL,
    entity_value   TEXT        NOT NULL,
    start_offset   INT         NOT NULL,
    end_offset     INT         NOT NULL,
    confidence     REAL        NOT NULL CHECK (confidence BETWEEN 0 AND 1),
    is_pii         BOOLEAN     NOT NULL DEFAULT false,
    detected_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);

CREATE INDEX IF NOT EXISTS idx_document_entities_version
    ON document_entities (tenant_id, version_id);
CREATE INDEX IF NOT EXISTS idx_document_entities_type
    ON document_entities (tenant_id, version_id, entity_type);
CREATE INDEX IF NOT EXISTS idx_document_entities_pii
    ON document_entities (tenant_id, version_id)
    WHERE is_pii = true;

ALTER TABLE document_entities ENABLE ROW LEVEL SECURITY;
DO $$ BEGIN
  IF NOT EXISTS (
      SELECT 1 FROM pg_policies
      WHERE tablename = 'document_entities'
        AND policyname = 'document_entities_tenant_isolation'
  ) THEN
    CREATE POLICY document_entities_tenant_isolation
        ON document_entities
        USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
  END IF;
END $$;


CREATE TABLE intel_processed_events (
    tenant_id     UUID        NOT NULL REFERENCES organizations(id),
    consumer      TEXT        NOT NULL
                  CHECK (consumer IN ('classify', 'ner', 'embed')),
    event_id      UUID        NOT NULL,
    document_id   UUID        NOT NULL,
    version_id    UUID        NOT NULL,
    status        TEXT        NOT NULL DEFAULT 'enqueued'
                  CHECK (status IN ('enqueued', 'completed', 'failed')),
    attempts      INT         NOT NULL DEFAULT 0,
    last_error    TEXT,
    processed_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at  TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, consumer, event_id)
);

CREATE INDEX idx_intel_processed_events_gc
    ON intel_processed_events (processed_at);

ALTER TABLE intel_processed_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY intel_processed_events_tenant_isolation
    ON intel_processed_events
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

COMMIT;
