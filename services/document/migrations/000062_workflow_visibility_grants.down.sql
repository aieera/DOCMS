DROP TABLE IF EXISTS workflow_grants;

DROP INDEX IF EXISTS idx_workflow_definitions_owner;
ALTER TABLE workflow_definitions DROP CONSTRAINT IF EXISTS workflow_definitions_tenant_id_owner_id_fkey;
ALTER TABLE workflow_definitions DROP COLUMN IF EXISTS owner_id;
ALTER TABLE workflow_definitions DROP COLUMN IF EXISTS visibility;
