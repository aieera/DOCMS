-- Per-device sync state for the selective-sync / virtual-drive client (§3/§5).
-- A device registers once, tracks its own delta cursor, and stores its
-- selective-sync folder set. The delta endpoint is served from documents/folders
-- keyset-paginated on (updated_at, id); this table only holds the client's
-- position + config, never the change feed itself.

CREATE TABLE IF NOT EXISTS sync_devices (
    tenant_id     UUID NOT NULL REFERENCES organizations(id),
    id            UUID NOT NULL DEFAULT gen_random_uuid(),
    user_id       UUID NOT NULL,                 -- the owner (api-key/session user)
    name          TEXT NOT NULL,                 -- "Alice's laptop"
    platform      TEXT NOT NULL DEFAULT '',      -- linux|macos|windows|...
    workspace_id  UUID,                          -- the workspace this device mirrors (NULL = all accessible)
    cursor        TEXT NOT NULL DEFAULT '',       -- opaque delta cursor (last acked position)
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at  TIMESTAMPTZ,
    revoked_at    TIMESTAMPTZ,
    revoked_by    UUID,
    PRIMARY KEY (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS idx_sync_devices_user ON sync_devices (tenant_id, user_id, created_at DESC);

ALTER TABLE sync_devices ENABLE ROW LEVEL SECURITY;
ALTER TABLE sync_devices FORCE ROW LEVEL SECURITY;
CREATE POLICY sync_devices_isolation ON sync_devices
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- Selective-sync folder set: the folders a device syncs. Empty set = sync the
-- whole workspace.
CREATE TABLE IF NOT EXISTS sync_device_folders (
    tenant_id  UUID NOT NULL REFERENCES organizations(id),
    device_id  UUID NOT NULL,
    folder_id  UUID NOT NULL,
    added_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, device_id, folder_id),
    FOREIGN KEY (tenant_id, device_id) REFERENCES sync_devices (tenant_id, id) ON DELETE CASCADE
);

ALTER TABLE sync_device_folders ENABLE ROW LEVEL SECURITY;
ALTER TABLE sync_device_folders FORCE ROW LEVEL SECURITY;
CREATE POLICY sync_device_folders_isolation ON sync_device_folders
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
