-- Wave 15.4 — Saved signatures (signature profiles).
--
-- One row per (tenant, user, profile). Image bytes live in object
-- storage; the row holds the S3 key + KMS-wrapped DEK per-profile so
-- crypto-shred on delete is a one-step operation.
--
-- `is_default` is enforced unique-per-user via a partial index —
-- Postgres doesn't let two rows for the same (tenant, user) have
-- is_default=true simultaneously. The service's SetDefault path
-- flips the previous default off in the same tx.

BEGIN;

CREATE TABLE IF NOT EXISTS signature_profiles (
    tenant_id         UUID NOT NULL,
    id                UUID NOT NULL DEFAULT gen_random_uuid(),
    user_id           UUID NOT NULL,
    name              TEXT NOT NULL,
    kind              TEXT NOT NULL
                          CHECK (kind IN ('draw', 'upload', 'typed')),
    font_style        TEXT,                   -- only for kind='typed'
    image_ref         TEXT NOT NULL,          -- S3 key inside the tenant's bucket prefix
    initials_ref      TEXT,                   -- optional secondary image
    wrapped_dek       BYTEA NOT NULL,         -- per-profile DEK wrapped with tenant KEK
    kek_id            TEXT NOT NULL,          -- which KEK wrapped the DEK
    nonce             BYTEA NOT NULL,         -- AES-GCM nonce for image ciphertext
    image_size_bytes  INTEGER NOT NULL,
    is_default        BOOLEAN NOT NULL DEFAULT false,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at        TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id)
);

-- Partial unique index: enforces "at most one default per user".
CREATE UNIQUE INDEX IF NOT EXISTS uniq_signature_profiles_default
    ON signature_profiles (tenant_id, user_id)
    WHERE is_default = true AND revoked_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_signature_profiles_user
    ON signature_profiles (tenant_id, user_id)
    WHERE revoked_at IS NULL;

ALTER TABLE signature_profiles ENABLE ROW LEVEL SECURITY;
ALTER TABLE signature_profiles FORCE  ROW LEVEL SECURITY;
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename='signature_profiles' AND policyname='signature_profiles_tenant_isolation') THEN
    EXECUTE 'CREATE POLICY signature_profiles_tenant_isolation ON signature_profiles
               USING (tenant_id = current_setting(''app.current_tenant'', true)::uuid)
               WITH CHECK (tenant_id = current_setting(''app.current_tenant'', true)::uuid)';
  END IF;
END $$;

CREATE TRIGGER trg_signature_profiles_updated_at
    BEFORE UPDATE ON signature_profiles FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

COMMIT;
