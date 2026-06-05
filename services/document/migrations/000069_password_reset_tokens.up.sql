-- Password-reset tokens (audit-2026-05 Track 2).
--
-- The opaque token handed to the user is "<tenant_id>.<random-hex>", so the
-- reset-password endpoint resolves the tenant from the token itself — no
-- cross-tenant lookup, every query stays tenant-scoped, and standard RLS holds
-- even under a NOBYPASSRLS prod role. We store sha256(full token); the
-- plaintext never lands in the DB. Single-use + 30-min expiry.
CREATE TABLE IF NOT EXISTS password_reset_tokens (
    tenant_id   uuid        NOT NULL REFERENCES organizations(id),
    user_id     uuid        NOT NULL,
    token_hash  text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    used_at     timestamptz,
    ip_address  inet,
    PRIMARY KEY (tenant_id, token_hash),
    FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id) ON DELETE CASCADE
);

-- Partial index keeps the active-token lookup cheap as used rows accumulate.
CREATE INDEX IF NOT EXISTS idx_reset_tokens_lookup
    ON password_reset_tokens (token_hash) WHERE used_at IS NULL;

ALTER TABLE password_reset_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE password_reset_tokens FORCE ROW LEVEL SECURITY;
CREATE POLICY reset_tokens_tenant_isolation ON password_reset_tokens
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
