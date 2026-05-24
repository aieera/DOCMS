-- Roll back 000056: drop the FK first (since it depends on the column),
-- then drop the column. Both IF EXISTS so partial application is recoverable.

ALTER TABLE tenant_metadata_schemas
  DROP CONSTRAINT IF EXISTS tenant_metadata_schemas_tenant_id_updated_by_fkey;

ALTER TABLE tenant_metadata_schemas DROP COLUMN IF EXISTS updated_by;
