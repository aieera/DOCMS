-- SCIM provisioning log (§8). Records each SCIM provisioning action so the
-- admin SCIM panel can show recent IdP activity (provision / update /
-- deprovision). Written best-effort by the auth service's SCIM handler; the
-- authoritative deprovision side effects (session/key revocation, the
-- dms.user.deprovisioned.v1 event) live in the SCIM DeactivateUser txn.
CREATE TABLE scim_provisioning_log (
    tenant_id     UUID        NOT NULL REFERENCES organizations(id),
    id            UUID        NOT NULL DEFAULT gen_random_uuid(),
    action        TEXT        NOT NULL
        CHECK (action IN ('provisioned', 'updated', 'deprovisioned', 'deleted')),
    resource_type TEXT        NOT NULL DEFAULT 'user'
        CHECK (resource_type IN ('user', 'group')),
    external_id   TEXT,                       -- the IdP's external id, when supplied
    user_id       UUID,                       -- the SeDoc user/group id affected
    detail        TEXT,                       -- human-readable summary (e.g. email)
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);
CREATE INDEX idx_scim_prov_log_time ON scim_provisioning_log (tenant_id, created_at DESC);

ALTER TABLE scim_provisioning_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE scim_provisioning_log FORCE  ROW LEVEL SECURITY;
CREATE POLICY scim_prov_log_tenant_isolation ON scim_provisioning_log
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY scim_prov_log_tenant_isolation_insert ON scim_provisioning_log
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
