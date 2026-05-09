-- Undo ADR 0064.
DROP TABLE IF EXISTS workflow_delegations;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_manager_fk;
DROP INDEX IF EXISTS idx_users_manager;
ALTER TABLE users DROP COLUMN IF EXISTS manager_id;
