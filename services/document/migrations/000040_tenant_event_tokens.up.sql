-- ADR 0077 — per-tenant streaming tokens.
--
-- Holds the hashed bearer tokens used by the polling endpoint
-- (/api/v1/events) and the NATS user JWT + nkey seed mailed back
-- to the admin once at issuance time.
--
-- The `token_hash` is a SHA-256 of the customer-visible bearer; we
-- never store the plaintext. `nats_user_jwt` and `nats_user_nkey_seed`
-- are stored at rest because the customer downloads a creds file
-- once + saves it themselves; if they lose it they regenerate (the
-- nkey seed is deemed not-secret-enough-to-encrypt at this layer
-- because possession of the row already implies tenant-admin access
-- via RLS — different tradeoff than the LLM-key AES path).

CREATE TABLE IF NOT EXISTS tenant_event_tokens (
    tenant_id            UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    id                   UUID NOT NULL DEFAULT gen_random_uuid(),

    -- Bearer half — for the polling endpoint. SHA-256 hex; the plaintext
    -- ('tev_<32 hex>') is shown to the operator exactly once at issuance.
    token_hash           TEXT NOT NULL,

    -- NATS half — operator-signed user JWT + nkey seed for direct NATS
    -- subscription. Only meaningful once the deploy switches to operator
    -- mode (VAULTDMS_NATS_OPERATOR_MODE=true); pre-issued tokens still
    -- become valid the moment the server rotates.
    nats_account_id      TEXT,
    nats_user_jwt        TEXT,
    nats_user_nkey_seed  TEXT,

    -- Operator metadata.
    label                TEXT NOT NULL,            -- "production receiver", "dev notebook", ...
    created_by           UUID,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at           TIMESTAMPTZ,              -- NULL = no expiry; default 30d set by service
    last_used_at         TIMESTAMPTZ,
    revoked_at           TIMESTAMPTZ,

    PRIMARY KEY (tenant_id, id)
);

ALTER TABLE tenant_event_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_event_tokens FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_event_tokens_tenant_isolation ON tenant_event_tokens
    USING (tenant_id = (current_setting('app.current_tenant', true))::uuid)
    WITH CHECK (tenant_id = (current_setting('app.current_tenant', true))::uuid);

CREATE INDEX IF NOT EXISTS idx_tenant_event_tokens_hash ON tenant_event_tokens (token_hash) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_tenant_event_tokens_tenant_active ON tenant_event_tokens (tenant_id, created_at DESC) WHERE revoked_at IS NULL;

GRANT SELECT, INSERT, UPDATE, DELETE ON tenant_event_tokens TO dms_app;
