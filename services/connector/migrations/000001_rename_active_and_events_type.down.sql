BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'webhook_subscriptions'
          AND column_name = 'events'
          AND udt_name  = 'jsonb'
    ) THEN
        -- JSONB → TEXT[]. We assume values are JSON arrays of strings
        -- (what the repository writes). translate() strips the JSON
        -- quoting; array_to_string + string_to_array round-trips into
        -- the TEXT[] format Postgres expects.
        ALTER TABLE webhook_subscriptions
            ALTER COLUMN events TYPE TEXT[]
            USING ARRAY(SELECT jsonb_array_elements_text(events))::TEXT[];
    END IF;
END $$;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'webhook_subscriptions' AND column_name = 'active'
    ) THEN
        ALTER TABLE webhook_subscriptions RENAME COLUMN active TO is_active;
    END IF;
END $$;

COMMIT;
