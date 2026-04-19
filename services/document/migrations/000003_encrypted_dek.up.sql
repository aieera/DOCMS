-- Envelope encryption: per-blob data-encryption key, wrapped by tenant KEK.
-- The plaintext DEK never touches the database; only the KMS-wrapped form
-- lands here. The KEK id is kept separately so rotation can scan for
-- blobs wrapped under an old KEK.

BEGIN;

ALTER TABLE content_blobs
    ADD COLUMN encrypted_dek BYTEA,
    ADD COLUMN dek_nonce     BYTEA,
    ADD COLUMN kek_id        TEXT;

-- Existing rows (there shouldn't be any yet) are marked as "not enveloped"
-- via NULL in encrypted_dek. Service-layer reads treat NULL encrypted_dek
-- as "read plaintext from S3" (legacy path until full migration).

COMMIT;
