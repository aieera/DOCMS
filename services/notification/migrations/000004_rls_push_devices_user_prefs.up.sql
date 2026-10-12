-- M-28 — RLS on the two notification tables that were created without it.
--
-- push_devices (000002) and notification_user_prefs (000003) carry
-- tenant_id but no row-level security; their DDL comments said the
-- service "queries the pool directly with explicit tenant_id predicates",
-- so one missing or wrong predicate read every tenant's push tokens and
-- preferences. They now get the schema-of-record pattern: ENABLE + FORCE
-- plus the tenant isolation policies keyed on app.current_tenant, and the
-- repository reaches them only inside database.WithTenantTx.
-- pkg/database.AssertRLSPosture also refuses to start a service while any
-- tenant table lacks FORCE.

ALTER TABLE push_devices ENABLE ROW LEVEL SECURITY;
ALTER TABLE push_devices FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS push_devices_tenant_isolation ON push_devices;
CREATE POLICY push_devices_tenant_isolation ON push_devices
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
DROP POLICY IF EXISTS push_devices_tenant_isolation_insert ON push_devices;
CREATE POLICY push_devices_tenant_isolation_insert ON push_devices
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

ALTER TABLE notification_user_prefs ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_user_prefs FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS notification_user_prefs_tenant_isolation ON notification_user_prefs;
CREATE POLICY notification_user_prefs_tenant_isolation ON notification_user_prefs
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
DROP POLICY IF EXISTS notification_user_prefs_tenant_isolation_insert ON notification_user_prefs;
CREATE POLICY notification_user_prefs_tenant_isolation_insert ON notification_user_prefs
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- The app role's privileges, stated rather than left to whatever default
-- privileges the migration role happened to have.
DO $do$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'dms_app') THEN
        EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON push_devices, notification_user_prefs TO dms_app';
    END IF;
END
$do$;
