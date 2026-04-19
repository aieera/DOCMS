BEGIN;
ALTER TABLE audit_events
    DROP COLUMN IF EXISTS source_event,
    DROP COLUMN IF EXISTS details,
    DROP COLUMN IF EXISTS resource_title;
COMMIT;
