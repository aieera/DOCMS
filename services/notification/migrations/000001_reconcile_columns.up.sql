-- §4.1 / A4 follow-up — notification service repo vs migration drift.
-- Repo code queries `type`, `read`, `channel`, `delivered_at`; the
-- original table in services/document/migrations/000001 uses
-- `event_type`, `is_read`, and lacks channel / delivered_at. This
-- blocks GET /api/v1/notifications with a 500.
--
-- Three renames + two additions. Idempotent via information_schema.

BEGIN;

-- event_type → type
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name='notifications' AND column_name='event_type'
    ) THEN
        ALTER TABLE notifications RENAME COLUMN event_type TO type;
    END IF;
END $$;

-- is_read → read
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name='notifications' AND column_name='is_read'
    ) THEN
        ALTER TABLE notifications RENAME COLUMN is_read TO read;
    END IF;
END $$;

-- channel (nullable — SMS/email/push/etc. not always known at write time)
ALTER TABLE notifications
    ADD COLUMN IF NOT EXISTS channel      TEXT,
    ADD COLUMN IF NOT EXISTS delivered_at TIMESTAMPTZ;

COMMIT;
