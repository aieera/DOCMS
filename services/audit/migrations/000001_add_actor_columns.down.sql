BEGIN;

DROP INDEX IF EXISTS idx_audit_events_actor;

ALTER TABLE audit_events
    DROP COLUMN IF EXISTS actor_name,
    DROP COLUMN IF EXISTS actor;

COMMIT;
