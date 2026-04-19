BEGIN;

ALTER TABLE notifications
    DROP COLUMN IF EXISTS delivered_at,
    DROP COLUMN IF EXISTS channel;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name='notifications' AND column_name='read'
    ) THEN
        ALTER TABLE notifications RENAME COLUMN read TO is_read;
    END IF;
END $$;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name='notifications' AND column_name='type'
    ) THEN
        ALTER TABLE notifications RENAME COLUMN type TO event_type;
    END IF;
END $$;

COMMIT;
