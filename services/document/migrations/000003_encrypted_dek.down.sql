BEGIN;
ALTER TABLE content_blobs
    DROP COLUMN IF EXISTS kek_id,
    DROP COLUMN IF EXISTS dek_nonce,
    DROP COLUMN IF EXISTS encrypted_dek;
COMMIT;
