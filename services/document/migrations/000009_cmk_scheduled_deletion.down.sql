BEGIN;
DROP INDEX IF EXISTS idx_tenant_keks_scheduled_deletion;
ALTER TABLE tenant_keks DROP COLUMN IF EXISTS scheduled_deletion_at;
ALTER TABLE tenant_keks DROP COLUMN IF EXISTS scheduled_by;
COMMIT;
