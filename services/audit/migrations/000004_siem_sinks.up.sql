-- SIEM forwarding sinks (§15). Per-tenant configuration for forwarding
-- normalised audit/domain events to an external SIEM: syslog, Splunk HEC, or
-- Microsoft Sentinel (HEC-style). Delivery-health counters are updated in-line
-- by the SIEM consumer so the admin UI can show success/failure without a
-- separate metrics store. Tenant-isolated via RLS like every audit table.
CREATE TABLE IF NOT EXISTS siem_sinks (
    tenant_id       UUID        NOT NULL,
    id              UUID        NOT NULL DEFAULT gen_random_uuid(),
    name            TEXT        NOT NULL,
    type            TEXT        NOT NULL CHECK (type IN ('syslog', 'splunk_hec', 'sentinel_hec')),
    endpoint        TEXT        NOT NULL,          -- host:port (syslog) or https URL (HEC)
    token           TEXT,                          -- HEC token / shared key (nullable for syslog)
    enabled         BOOLEAN     NOT NULL DEFAULT true,
    -- delivery health, bumped by the consumer
    delivered_count BIGINT      NOT NULL DEFAULT 0,
    failed_count    BIGINT      NOT NULL DEFAULT 0,
    last_success_at TIMESTAMPTZ,
    last_error      TEXT,
    last_error_at   TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);

CREATE INDEX IF NOT EXISTS idx_siem_sinks_enabled ON siem_sinks (tenant_id) WHERE enabled;

ALTER TABLE siem_sinks ENABLE ROW LEVEL SECURITY;
ALTER TABLE siem_sinks FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS siem_sinks_tenant_isolation ON siem_sinks;
CREATE POLICY siem_sinks_tenant_isolation ON siem_sinks
    FOR ALL
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
