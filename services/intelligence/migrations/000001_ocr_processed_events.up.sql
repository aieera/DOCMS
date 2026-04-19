-- Dedupe table for the OCR consumer. Every incoming NATS message
-- carries a UUIDv7 event_id set by the publisher (pkg/database/outbox).
-- At-least-once delivery means the broker may redeliver the same
-- message (restart, partial ack, consumer-side crash). The consumer
-- checks this table before enqueueing work; if the event_id is
-- already present, the redelivery is ACKed and dropped.
--
-- Index plan:
--   * PK (tenant_id, event_id) gives the O(log n) dedupe lookup.
--   * (processed_at) supports the nightly GC that drops rows older
--     than 14 days — keeps table size bounded without touching
--     pg_cron (manual or Temporal cron per Wave 8.1).
--   * All queries MUST be issued with app.current_tenant set so RLS
--     narrows to the caller's tenant only.
BEGIN;

CREATE TABLE ocr_processed_events (
    tenant_id     UUID        NOT NULL REFERENCES organizations(id),
    event_id      UUID        NOT NULL,
    document_id   UUID        NOT NULL,
    version_id    UUID        NOT NULL,
    status        TEXT        NOT NULL DEFAULT 'enqueued'
                  CHECK (status IN ('enqueued', 'completed', 'failed')),
    attempts      INT         NOT NULL DEFAULT 0,
    last_error    TEXT,
    processed_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at  TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, event_id)
);

CREATE INDEX idx_ocr_processed_events_gc
    ON ocr_processed_events (processed_at);

ALTER TABLE ocr_processed_events ENABLE ROW LEVEL SECURITY;

CREATE POLICY ocr_processed_events_tenant_isolation
    ON ocr_processed_events
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

COMMIT;
