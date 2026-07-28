-- Reverts the schema pieces only. The membership backfill is data the
-- tenant now relies on (users would silently lose their default
-- workspace) — intentionally NOT deleted on down.
DROP INDEX IF EXISTS workspaces_one_default_per_tenant;
ALTER TABLE workspaces DROP COLUMN IF EXISTS is_default;
