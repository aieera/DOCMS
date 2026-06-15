-- Backfill of existing ERP data into SeDoc. A run enumerates customers + their
-- documents from a source (the ERP listing API or an NDJSON file), synthesizes
-- the canonical events, and feeds them through the SAME idempotent sync handlers
-- — so re-runs create nothing new. These tables track run status/progress and
-- record per-item failures.

CREATE TABLE backfill_runs (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    status      TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending','running','completed','failed','canceled')),
    source      TEXT NOT NULL DEFAULT 'erp',   -- 'erp' (listing API) | 'ndjson' (file)
    source_arg  TEXT NOT NULL DEFAULT '',        -- erp: optional comma-separated customer_refs to scope; ndjson: file path
    total       INT NOT NULL DEFAULT 0,          -- planned work items (customers + their documents)
    processed   INT NOT NULL DEFAULT 0,          -- items finished (success or failure)
    failed      INT NOT NULL DEFAULT 0,          -- subset of processed that errored
    last_error  TEXT NOT NULL DEFAULT '',        -- run-level fatal error (enumeration / cancellation)
    started_at  TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- The worker claims the oldest pending run (BFF-triggered backfills land here).
CREATE INDEX idx_backfill_runs_pending ON backfill_runs (created_at) WHERE status = 'pending';
CREATE INDEX idx_backfill_runs_recent ON backfill_runs (created_at DESC);

-- One row per failed work item, for the dashboard's per-item failure drill-down.
CREATE TABLE backfill_failures (
    id             BIGSERIAL PRIMARY KEY,
    run_id         UUID NOT NULL REFERENCES backfill_runs(id) ON DELETE CASCADE,
    customer_ref   TEXT NOT NULL DEFAULT '',
    item           TEXT NOT NULL DEFAULT '',   -- e.g. "customer.created", "invoice-188", "attachment:contract.pdf"
    error          TEXT NOT NULL DEFAULT '',
    correlation_id TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_backfill_failures_run ON backfill_failures (run_id, created_at);
