DROP TABLE IF EXISTS outbox_dlq;
DROP INDEX IF EXISTS idx_outbox_due;
ALTER TABLE outbox
    DROP COLUMN IF EXISTS next_attempt_at,
    DROP COLUMN IF EXISTS last_error,
    DROP COLUMN IF EXISTS attempts;
