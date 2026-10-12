-- Single-use OAuth state nonces for the e-sign (DocuSign / Adobe Sign)
-- connect flow — review finding S-2.
--
-- The OAuth `state` used to be a deterministic, non-expiring
-- HMAC(tenant|provider): the same value for every connect attempt of a
-- tenant, replayable forever, and not bound to the admin who started the
-- flow. Any member could therefore complete a connect and bind their own
-- vendor account to the tenant.
--
-- The signature service now mints a random 256-bit nonce per
-- /esign/oauth/start, stores only its SHA-256 here bound to
-- (tenant, user, provider) with a 10-minute expiry, and the callback
-- consumes it with a single DELETE … RETURNING scoped to the calling
-- session's tenant and user. A replayed, expired, foreign or forged state
-- matches no row.

CREATE TABLE IF NOT EXISTS esign_oauth_states (
    tenant_id   UUID        NOT NULL REFERENCES organizations(id),
    nonce_hash  TEXT        NOT NULL,           -- hex SHA-256 of the state nonce
    user_id     UUID        NOT NULL,           -- the admin who started the flow
    provider    TEXT        NOT NULL CHECK (provider IN ('docusign','adobe_sign')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, nonce_hash),
    FOREIGN KEY (tenant_id, user_id) REFERENCES users(tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_esign_oauth_states_expiry
    ON esign_oauth_states (tenant_id, expires_at);

ALTER TABLE esign_oauth_states ENABLE ROW LEVEL SECURITY;
ALTER TABLE esign_oauth_states FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS esign_oauth_states_tenant_isolation        ON esign_oauth_states;
DROP POLICY IF EXISTS esign_oauth_states_tenant_isolation_insert ON esign_oauth_states;
CREATE POLICY esign_oauth_states_tenant_isolation ON esign_oauth_states
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY esign_oauth_states_tenant_isolation_insert ON esign_oauth_states
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dms_app') THEN
        GRANT SELECT, INSERT, DELETE ON esign_oauth_states TO dms_app;
    END IF;
END $$;
