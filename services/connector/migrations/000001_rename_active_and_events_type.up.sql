-- §4.1 / A4 — reconcile webhook_subscriptions schema with connector's
-- shipped repository code (/api/v1/webhooks has been returning 500).
--
-- Two drifts:
--
--   1. Column `is_active BOOLEAN` → repository reads/writes `active`.
--   2. Column `events TEXT[]`     → repository stores JSON bytes and
--      uses the @> JSONB containment operator for event-type filtering,
--      so the column must be JSONB.
--
-- Rename + retype is safe:
--   * The partial index `idx_webhook_subs_active` uses a WHERE clause
--     on is_active; Postgres rewrites the predicate on RENAME.
--   * to_jsonb(TEXT[]) converts cleanly to a JSON array of strings,
--     matching the format the repository writes today.
--
-- Idempotent: both blocks are guarded by information_schema lookups,
-- so a re-run is a no-op.

BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'webhook_subscriptions' AND column_name = 'is_active'
    ) THEN
        ALTER TABLE webhook_subscriptions RENAME COLUMN is_active TO active;
    END IF;
END $$;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'webhook_subscriptions'
          AND column_name = 'events'
          AND udt_name  = '_text'
    ) THEN
        ALTER TABLE webhook_subscriptions
            ALTER COLUMN events TYPE JSONB USING to_jsonb(events);
    END IF;
END $$;

COMMIT;
