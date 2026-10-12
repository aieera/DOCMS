ALTER TABLE upload_sessions
    DROP COLUMN IF EXISTS storage_key,
    DROP COLUMN IF EXISTS storage_bucket;
