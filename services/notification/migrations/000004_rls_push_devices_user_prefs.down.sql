-- Reverse of 000004. The grants are left in place (they predate it in
-- every environment that used default privileges).
DROP POLICY IF EXISTS notification_user_prefs_tenant_isolation_insert ON notification_user_prefs;
DROP POLICY IF EXISTS notification_user_prefs_tenant_isolation ON notification_user_prefs;
ALTER TABLE notification_user_prefs NO FORCE ROW LEVEL SECURITY;
ALTER TABLE notification_user_prefs DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS push_devices_tenant_isolation_insert ON push_devices;
DROP POLICY IF EXISTS push_devices_tenant_isolation ON push_devices;
ALTER TABLE push_devices NO FORCE ROW LEVEL SECURITY;
ALTER TABLE push_devices DISABLE ROW LEVEL SECURITY;
