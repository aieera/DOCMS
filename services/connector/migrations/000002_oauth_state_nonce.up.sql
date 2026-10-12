-- Server-side, single-use OAuth state for native connectors (Google,
-- Microsoft 365).
--
-- The connector OAuth `state` was a stateless HMAC over
-- tenant|connector|nonce|exp: unpredictable and expiring, but replayable
-- for its whole 10-minute window and not bound to the browser that
-- started the flow. Each authorize now records its nonce here together
-- with the PKCE code_verifier; the callback deletes the row (single use)
-- and sends the verifier with the code exchange, so a replayed or
-- injected state/code is rejected.
--
-- Tenant table: tenant_id leads the PK and FORCE RLS applies like every
-- other connector table. The callback learns the tenant from the
-- HMAC-verified state, so the consume runs inside WithTenantTx.

BEGIN;

CREATE TABLE IF NOT EXISTS connector_oauth_states (
    tenant_id      UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    nonce          TEXT        NOT NULL,
    connector_type TEXT        NOT NULL,
    code_verifier  TEXT        NOT NULL,
    created_by     UUID,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at     TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, nonce)
);

CREATE INDEX IF NOT EXISTS idx_connector_oauth_states_expires
    ON connector_oauth_states (tenant_id, expires_at);

ALTER TABLE connector_oauth_states ENABLE ROW LEVEL SECURITY;
ALTER TABLE connector_oauth_states FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS connector_oauth_states_tenant_isolation ON connector_oauth_states;
CREATE POLICY connector_oauth_states_tenant_isolation ON connector_oauth_states
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dms_app') THEN
        GRANT SELECT, INSERT, DELETE ON connector_oauth_states TO dms_app;
    END IF;
END $$;

COMMIT;
