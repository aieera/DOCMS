DROP INDEX IF EXISTS idx_quarantine_events_status;
ALTER TABLE quarantine_events
    DROP COLUMN IF EXISTS reviewed_at,
    DROP COLUMN IF EXISTS reviewed_by,
    DROP COLUMN IF EXISTS status;
