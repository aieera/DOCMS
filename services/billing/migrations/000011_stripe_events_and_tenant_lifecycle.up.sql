-- Wave 20 — control plane:
--   1. stripe_events  : idempotency guard. Stripe retries webhooks
--      up to 3 days; duplicate event_id → no-op at the handler edge.
--   2. organizations.{dispose_scheduled_at, disposed_at} : 30-day
--      soft-delete → hard-dispose lifecycle. deleted_at already
--      exists (marks soft-delete moment); these track the
--      downstream transitions.
BEGIN;

CREATE TABLE IF NOT EXISTS stripe_events (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Stripe's own event id. UNIQUE is the idempotency contract.
    stripe_event_id TEXT NOT NULL UNIQUE,
    event_type      TEXT NOT NULL,
    received_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    payload         JSONB NOT NULL
);

-- GC index — operators sweep old events nightly; 90 days retention
-- covers Stripe's max redelivery window plus a month of audit.
CREATE INDEX IF NOT EXISTS idx_stripe_events_received_at
    ON stripe_events(received_at);

ALTER TABLE organizations
    ADD COLUMN IF NOT EXISTS dispose_scheduled_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS disposed_at          TIMESTAMPTZ;

-- Fast lookup of tenants due for hard-dispose. Partial index so the
-- nightly sweep doesn't scan the full table.
CREATE INDEX IF NOT EXISTS idx_organizations_dispose_due
    ON organizations(dispose_scheduled_at)
 WHERE disposed_at IS NULL AND dispose_scheduled_at IS NOT NULL;

COMMIT;
