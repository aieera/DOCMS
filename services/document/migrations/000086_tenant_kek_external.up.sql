-- External-KMS key reference on the per-tenant KEK (§5/§8). A tenant can
-- register an external KMS key (Vault transit / AWS KMS / Azure Key Vault) as
-- their KEK source; the existing per-tenant kekID/alias stays the wrap handle,
-- and these columns record which external key backs each version + support a
-- documented break-glass (revoke).
ALTER TABLE tenant_keks
    ADD COLUMN IF NOT EXISTS provider         TEXT NOT NULL DEFAULT 'local'
        CHECK (provider IN ('local', 'vault', 'aws_kms', 'azure_kv')),
    ADD COLUMN IF NOT EXISTS external_key_ref TEXT,            -- ARN / transit path / Key Vault URI
    ADD COLUMN IF NOT EXISTS revoked_at       TIMESTAMPTZ,     -- break-glass: version is unusable
    ADD COLUMN IF NOT EXISTS revoked_by       UUID;
