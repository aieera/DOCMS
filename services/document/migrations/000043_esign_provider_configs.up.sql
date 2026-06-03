-- Per-tenant DocuSign / Adobe Sign OAuth client credentials.
--
-- Before this: provider client_id / client_secret lived in env vars
-- (SEDOC_ESIGN_DOCUSIGN_* / *_ADOBE_SIGN_*) — one set for the whole
-- deployment. Multi-tenant DMS needs each tenant to register their own
-- integration app with the vendor, so creds are per (tenant, provider).
--
-- client_secret is sealed with the per-tenant KEK (same envelope as
-- esign_oauth_tokens.access_token); a Postgres dump alone cannot yield
-- usable secrets.
CREATE TABLE IF NOT EXISTS esign_provider_configs (
    tenant_id              UUID         NOT NULL REFERENCES organizations(id),
    provider               TEXT         NOT NULL CHECK (provider IN ('docusign','adobe_sign')),
    client_id              TEXT         NOT NULL,
    client_secret_sealed   TEXT         NOT NULL,
    -- 'sandbox' resolves to vendor demo hosts (account-d.docusign.com,
    -- secure.na1.adobesign.com); 'production' resolves to the live ones.
    -- The service layer picks the right authorize_url/token_url from
    -- this column so the user never pastes wrong URLs.
    environment            TEXT         NOT NULL CHECK (environment IN ('sandbox','production')),
    -- Optional override — if set, takes precedence over the
    -- environment-derived defaults. Lets a tenant point at a private
    -- DocuSign cluster or an Adobe Sign region we haven't enumerated.
    authorize_url_override TEXT,
    token_url_override     TEXT,
    -- Optional Adobe region hint (na1/eu1/jp1). Ignored for DocuSign.
    region                 TEXT,
    configured_by          UUID,
    created_at             TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, provider),
    FOREIGN KEY (tenant_id, configured_by) REFERENCES users(tenant_id, id)
);

CREATE TRIGGER update_esign_provider_configs_updated_at
    BEFORE UPDATE ON esign_provider_configs FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

ALTER TABLE esign_provider_configs ENABLE ROW LEVEL SECURITY;
ALTER TABLE esign_provider_configs FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS esign_provider_configs_tenant_isolation        ON esign_provider_configs;
DROP POLICY IF EXISTS esign_provider_configs_tenant_isolation_insert ON esign_provider_configs;
CREATE POLICY esign_provider_configs_tenant_isolation ON esign_provider_configs
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY esign_provider_configs_tenant_isolation_insert ON esign_provider_configs
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
