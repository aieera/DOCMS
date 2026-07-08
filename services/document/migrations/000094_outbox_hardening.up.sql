-- Wave 0.2 — harden the shared outbox drain against the wedge.
--
-- OutboxPublisher.drainBatch used to STOP on the first publish failure,
-- so one row whose event_type had no bound JetStream stream ("no
-- responders") blocked every event behind it forever. The drain now
-- retries per-row with backoff and dead-letters exhausted rows, which
-- needs per-row bookkeeping on outbox plus a durable dead-letter table.

ALTER TABLE outbox
    ADD COLUMN IF NOT EXISTS attempts        INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_error      TEXT,
    ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMPTZ;

-- The drain now polls WHERE NOT published AND (next_attempt_at IS NULL OR
-- next_attempt_at <= now()); index that so backoff-delayed rows don't
-- bloat the scan.
CREATE INDEX IF NOT EXISTS idx_outbox_due
    ON outbox (next_attempt_at)
    WHERE NOT published;

-- Dead-letter: a row that failed to publish maxAttempts times is moved
-- here (out of the active outbox so the drain advances) with the last
-- error. Operators inspect + replay from here; outbox_dlq_depth alerts
-- when it is non-empty. Mirrors the outbox columns + failure metadata.
CREATE TABLE IF NOT EXISTS outbox_dlq (
    id               UUID PRIMARY KEY,
    tenant_id        UUID NOT NULL,
    event_type       TEXT NOT NULL,
    aggregate_type   TEXT NOT NULL,
    aggregate_id     UUID NOT NULL,
    payload          JSONB NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL,
    actor_id         UUID,
    actor_name       TEXT,
    ip_address       INET,
    user_agent       TEXT,
    attempts         INT NOT NULL,
    last_error       TEXT,
    dead_lettered_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_outbox_dlq_event_type ON outbox_dlq (event_type);

-- The drain runs process-wide (not tenant-scoped): it publishes every
-- tenant's events. outbox itself is FORCE RLS, but the publisher role
-- reads it via the same path the existing drain uses (issue #88 tracks
-- moving the drain to a per-tenant / definer read). outbox_dlq follows
-- outbox: no RLS, drained/inspected by the platform publisher only.
