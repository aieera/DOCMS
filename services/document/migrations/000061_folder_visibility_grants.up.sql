-- Phase 2 of the workspace-folders feature: per-folder ACL.
--
-- Adds:
--   1. folders.visibility — 'shared' (default, inherits workspace
--      membership) | 'private' (owner + grants only). NOT NULL with
--      default 'shared' so legacy rows + new auto-roots keep behaving
--      as they always have.
--   2. folders.owner_id — populated for private folders at creation
--      time. NULL is allowed for shared folders so a tenant-wide
--      audit / admin operation doesn't lose ownership semantics for
--      shared rows where the concept doesn't apply.
--   3. folder_grants — folder-scoped ACL. (grantee_type, grantee_id)
--      pairs are unique per folder so a re-grant is an upsert /
--      no-op rather than a duplicate row.

ALTER TABLE folders
    ADD COLUMN visibility TEXT NOT NULL DEFAULT 'shared'
        CHECK (visibility IN ('shared', 'private')),
    ADD COLUMN owner_id   UUID;

ALTER TABLE folders
    ADD CONSTRAINT folders_tenant_id_owner_id_fkey
        FOREIGN KEY (tenant_id, owner_id)
        REFERENCES users(tenant_id, id);

-- Index on (tenant_id, owner_id) so the "private folders I own"
-- listing in the workspace view can scan quickly.
CREATE INDEX idx_folders_owner ON folders(tenant_id, owner_id) WHERE owner_id IS NOT NULL;

CREATE TABLE folder_grants (
    tenant_id    UUID        NOT NULL REFERENCES organizations(id),
    id           UUID        NOT NULL DEFAULT gen_random_uuid(),
    folder_id    UUID        NOT NULL,
    grantee_type TEXT        NOT NULL CHECK (grantee_type IN ('user', 'group')),
    grantee_id   UUID        NOT NULL,
    granted_by   UUID,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, folder_id, grantee_type, grantee_id),
    FOREIGN KEY (tenant_id, folder_id)  REFERENCES folders(tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, granted_by) REFERENCES users(tenant_id, id)
);

CREATE INDEX idx_folder_grants_folder  ON folder_grants(tenant_id, folder_id);
CREATE INDEX idx_folder_grants_grantee ON folder_grants(tenant_id, grantee_type, grantee_id);

ALTER TABLE folder_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE folder_grants FORCE  ROW LEVEL SECURITY;
CREATE POLICY folder_grants_tenant_isolation ON folder_grants
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY folder_grants_tenant_isolation_insert ON folder_grants
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
