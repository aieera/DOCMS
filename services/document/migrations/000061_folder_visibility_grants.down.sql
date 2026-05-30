DROP TABLE IF EXISTS folder_grants;

DROP INDEX IF EXISTS idx_folders_owner;
ALTER TABLE folders DROP CONSTRAINT IF EXISTS folders_tenant_id_owner_id_fkey;
ALTER TABLE folders DROP COLUMN IF EXISTS owner_id;
ALTER TABLE folders DROP COLUMN IF EXISTS visibility;
