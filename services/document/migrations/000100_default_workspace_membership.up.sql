-- Default-workspace visibility model:
--   * exactly one workspace per tenant may be flagged is_default;
--   * every tenant user is a member of it (backfilled here; kept
--     current for new users by the document service's
--     dms.user.created.v1 consumer);
--   * the workspace list/get paths additionally treat is_default as
--     visible-to-all so a brand-new user always sees exactly one
--     workspace even before the membership row lands.

ALTER TABLE workspaces ADD COLUMN IF NOT EXISTS is_default boolean NOT NULL DEFAULT false;

-- Pick the earliest live "Default Workspace" per tenant (the
-- onboarding-created one).
WITH pick AS (
  SELECT DISTINCT ON (tenant_id) tenant_id, id
    FROM workspaces
   WHERE deleted_at IS NULL AND name = 'Default Workspace'
   ORDER BY tenant_id, created_at ASC
)
UPDATE workspaces w
   SET is_default = true
  FROM pick
 WHERE w.tenant_id = pick.tenant_id AND w.id = pick.id;

-- One default per tenant, enforced.
CREATE UNIQUE INDEX IF NOT EXISTS workspaces_one_default_per_tenant
    ON workspaces (tenant_id) WHERE is_default AND deleted_at IS NULL;

-- Everyone is a member of their tenant's default workspace.
INSERT INTO workspace_members (tenant_id, workspace_id, user_id, role, added_by)
SELECT u.tenant_id, w.id, u.id, 'member', NULL
  FROM users u
  JOIN workspaces w
    ON w.tenant_id = u.tenant_id AND w.is_default AND w.deleted_at IS NULL
 WHERE u.deleted_at IS NULL
ON CONFLICT (tenant_id, workspace_id, user_id) DO NOTHING;
