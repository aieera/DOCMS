BEGIN;
DROP POLICY IF EXISTS outbox_tenant_isolation ON outbox;
DROP INDEX IF EXISTS idx_outbox_tenant_time;
DROP INDEX IF EXISTS idx_outbox_unpublished;
DROP TABLE IF EXISTS outbox;
COMMIT;
