BEGIN;
DROP POLICY IF EXISTS tenant_keks_tenant_isolation ON tenant_keks;
DROP INDEX IF EXISTS tenant_keks_one_live_per_tenant;
DROP TABLE IF EXISTS tenant_keks;
COMMIT;
