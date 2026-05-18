DROP TRIGGER IF EXISTS update_tenant_smtp_configs_updated_at   ON tenant_smtp_configs;
DROP TRIGGER IF EXISTS update_tenant_twilio_configs_updated_at ON tenant_twilio_configs;
DROP TABLE IF EXISTS tenant_smtp_configs;
DROP TABLE IF EXISTS tenant_twilio_configs;
