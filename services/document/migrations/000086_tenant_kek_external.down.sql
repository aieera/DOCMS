ALTER TABLE tenant_keks
    DROP COLUMN IF EXISTS provider,
    DROP COLUMN IF EXISTS external_key_ref,
    DROP COLUMN IF EXISTS revoked_at,
    DROP COLUMN IF EXISTS revoked_by;
