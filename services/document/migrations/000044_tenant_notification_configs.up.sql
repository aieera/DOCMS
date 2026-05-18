-- Per-tenant Twilio (SMS MFA) + SMTP (transactional + email OTP)
-- credentials, mirroring the esign_provider_configs pattern.
--
-- Both auth and notification services read these. The shared
-- Postgres + RLS-by-tenant pattern means each service runs queries
-- inside its own database.WithTenant scope; cross-service reads
-- don't need a gRPC hop.
--
-- Secret columns are sealed with the per-tenant KEK via pkg/esign
-- SealString (same envelope as esign_oauth_tokens).

-- ---- 1. tenant_twilio_configs ------------------------------------
CREATE TABLE IF NOT EXISTS tenant_twilio_configs (
    tenant_id           UUID         NOT NULL REFERENCES organizations(id),
    account_sid         TEXT         NOT NULL,
    auth_token_sealed   TEXT         NOT NULL,
    verify_service_sid  TEXT         NOT NULL,
    updated_by          UUID,
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id),
    FOREIGN KEY (tenant_id, updated_by) REFERENCES users(tenant_id, id)
);

CREATE TRIGGER update_tenant_twilio_configs_updated_at
    BEFORE UPDATE ON tenant_twilio_configs FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

ALTER TABLE tenant_twilio_configs ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_twilio_configs FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_twilio_configs_iso        ON tenant_twilio_configs;
DROP POLICY IF EXISTS tenant_twilio_configs_iso_insert ON tenant_twilio_configs;
CREATE POLICY tenant_twilio_configs_iso ON tenant_twilio_configs
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY tenant_twilio_configs_iso_insert ON tenant_twilio_configs
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);


-- ---- 2. tenant_smtp_configs --------------------------------------
CREATE TABLE IF NOT EXISTS tenant_smtp_configs (
    tenant_id        UUID         NOT NULL REFERENCES organizations(id),
    host             TEXT         NOT NULL,
    port             INT          NOT NULL DEFAULT 587,
    username         TEXT         NOT NULL DEFAULT '',
    password_sealed  TEXT         NOT NULL DEFAULT '',
    from_addr        TEXT         NOT NULL,
    starttls         BOOLEAN      NOT NULL DEFAULT TRUE,
    updated_by       UUID,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id),
    FOREIGN KEY (tenant_id, updated_by) REFERENCES users(tenant_id, id)
);

CREATE TRIGGER update_tenant_smtp_configs_updated_at
    BEFORE UPDATE ON tenant_smtp_configs FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

ALTER TABLE tenant_smtp_configs ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_smtp_configs FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_smtp_configs_iso        ON tenant_smtp_configs;
DROP POLICY IF EXISTS tenant_smtp_configs_iso_insert ON tenant_smtp_configs;
CREATE POLICY tenant_smtp_configs_iso ON tenant_smtp_configs
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY tenant_smtp_configs_iso_insert ON tenant_smtp_configs
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
