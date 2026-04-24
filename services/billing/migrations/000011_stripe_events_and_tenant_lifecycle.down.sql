BEGIN;
DROP INDEX IF EXISTS idx_organizations_dispose_due;
ALTER TABLE organizations
    DROP COLUMN IF EXISTS disposed_at,
    DROP COLUMN IF EXISTS dispose_scheduled_at;
DROP INDEX IF EXISTS idx_stripe_events_received_at;
DROP TABLE IF EXISTS stripe_events;
COMMIT;
