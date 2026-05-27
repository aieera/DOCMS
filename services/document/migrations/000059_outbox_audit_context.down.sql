ALTER TABLE outbox
    DROP COLUMN IF EXISTS actor_id,
    DROP COLUMN IF EXISTS actor_name,
    DROP COLUMN IF EXISTS ip_address;
