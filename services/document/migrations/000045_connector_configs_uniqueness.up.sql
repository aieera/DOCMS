-- One config per (tenant, connector_type). The repo's UpsertConnector
-- relies on this for ON CONFLICT — the existing schema only had an
-- index, not a unique constraint, so the upsert would fail at runtime.
ALTER TABLE connector_configs
    ADD CONSTRAINT connector_configs_tenant_type_unique
    UNIQUE (tenant_id, connector_type);
