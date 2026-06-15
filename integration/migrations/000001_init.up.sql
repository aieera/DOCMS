-- Integration worker state (its OWN database — separate from SeDoc). The
-- integration acts as a single SeDoc tenant via the service API key, so no RLS
-- here; SeDoc enforces tenant isolation on its side.

-- Shared bucket folders (one per shard, reused across many customers). Unique on
-- (workspace_id, bucket_label) so concurrent provisioning converges on one.
CREATE TABLE folder_buckets (
    workspace_id  TEXT NOT NULL,
    bucket_label  TEXT NOT NULL,
    folder_id     TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, bucket_label)
);

-- One row per customer: the resolved SeDoc folder ids.
CREATE TABLE customer_map (
    customer_ref     TEXT PRIMARY KEY,
    name             TEXT NOT NULL DEFAULT '',
    bucket_folder_id TEXT NOT NULL,
    main_folder_id   TEXT NOT NULL,
    subfolder_ids    JSONB NOT NULL DEFAULT '{}'::jsonb,  -- {quote,po,so,do,invoice,attachments}
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One row per ERP event. id is the SeDoc Idempotency-Key seed (stable). The
-- unique erp_event_id makes webhook ingestion at-least-once safe.
CREATE TABLE sync_log (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    erp_event_id    TEXT NOT NULL UNIQUE,
    kind            TEXT NOT NULL,
    customer_ref    TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending','done','failed')),
    attempts        INT NOT NULL DEFAULT 0,
    payload         JSONB NOT NULL,
    sedoc_result    JSONB,
    correlation_id  TEXT NOT NULL DEFAULT '',
    last_error      TEXT NOT NULL DEFAULT '',
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- The worker claims due pending rows; the DLQ view filters status='failed'.
CREATE INDEX idx_sync_log_due ON sync_log (next_attempt_at) WHERE status = 'pending';
CREATE INDEX idx_sync_log_status ON sync_log (status, created_at DESC);

-- Tracks /ingest items so the review UI can show their SeDoc state.
CREATE TABLE ingestion_tracking (
    ingestion_item_id TEXT PRIMARY KEY,
    customer_ref      TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'received',
    sedoc_state       JSONB,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
