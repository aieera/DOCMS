-- Roll back 000057: remove the singleton-tenant constraint + name default.

ALTER TABLE tenant_metadata_schemas
  DROP CONSTRAINT IF EXISTS tenant_metadata_schemas_tenant_id_singleton;

ALTER TABLE tenant_metadata_schemas
  ALTER COLUMN name DROP DEFAULT;
