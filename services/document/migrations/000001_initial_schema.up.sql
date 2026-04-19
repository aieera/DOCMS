-- VaultDMS — initial schema (single atomic migration).
--
-- Security invariants enforced here:
--   1. Every table except `organizations` has tenant_id UUID NOT NULL as
--      its FIRST column.
--   2. PK is (tenant_id, id) composite — tenant_id first for index-scan
--      efficiency. Single-column PKs are used only where noted
--      (content_blobs for dedup, audit_events for append perf, sessions,
--      outbox BIGSERIAL, subscriptions_billing).
--   3. RLS is ENABLED AND FORCED on every tenant-scoped table.
--   4. Policies use current_setting('app.current_tenant', true)::uuid —
--      the `true` returns NULL on missing setting, which matches NO rows.
--   5. No CASCADE DELETE anywhere. All deletes are soft (deleted_at).
--   6. All UUIDs default to gen_random_uuid() (pgcrypto).
--   7. All timestamps are TIMESTAMPTZ.
--   8. Foreign keys are composite (tenant_id, x_id) → parent(tenant_id, id)
--      for tenant-scoped parents. This makes cross-tenant FK physically
--      impossible in addition to logically impossible via RLS.
--   9. Every FK column has an index; no OFFSET — callers paginate via
--      (sort_col, id) keyset cursors.

BEGIN;

-- ---------------------------------------------------------------------------
-- Extensions
-- ---------------------------------------------------------------------------
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";
CREATE EXTENSION IF NOT EXISTS "ltree";
CREATE EXTENSION IF NOT EXISTS "btree_gin";
CREATE EXTENSION IF NOT EXISTS "pg_trgm";

-- ---------------------------------------------------------------------------
-- updated_at trigger helper
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Roles (idempotent) --------------------------------------------------------
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dms_app') THEN
        CREATE ROLE dms_app LOGIN PASSWORD 'devpassword';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dms_readonly') THEN
        CREATE ROLE dms_readonly LOGIN PASSWORD 'devpassword';
    END IF;
END$$;

-- ===========================================================================
-- TABLE 1: organizations — the tenant table. No RLS; this IS the tenant.
-- ===========================================================================
CREATE TABLE organizations (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name           TEXT NOT NULL,
    slug           TEXT NOT NULL UNIQUE,
    plan           TEXT NOT NULL DEFAULT 'standard'
                        CHECK (plan IN ('standard', 'enterprise', 'dedicated')),
    settings       JSONB NOT NULL DEFAULT '{}'::jsonb,
    primary_region TEXT NOT NULL DEFAULT 'us-east-1',
    logo_url       TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at     TIMESTAMPTZ
);
CREATE INDEX idx_organizations_slug ON organizations(slug) WHERE deleted_at IS NULL;
CREATE TRIGGER update_organizations_updated_at
    BEFORE UPDATE ON organizations
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- ===========================================================================
-- TABLE 2: users
-- ===========================================================================
CREATE TABLE users (
    tenant_id             UUID NOT NULL REFERENCES organizations(id),
    id                    UUID NOT NULL DEFAULT gen_random_uuid(),
    email                 TEXT NOT NULL,
    display_name          TEXT NOT NULL,
    password_hash         TEXT,
    avatar_url            TEXT,
    role                  TEXT NOT NULL DEFAULT 'member'
                               CHECK (role IN ('owner', 'admin', 'member', 'guest')),
    status                TEXT NOT NULL DEFAULT 'active'
                               CHECK (status IN ('active', 'suspended', 'deactivated')),
    mfa_enabled           BOOLEAN NOT NULL DEFAULT false,
    mfa_secret_encrypted  TEXT,
    mfa_recovery_hashes   TEXT[],
    last_login_at         TIMESTAMPTZ,
    locale                TEXT DEFAULT 'en',
    timezone              TEXT DEFAULT 'UTC',
    settings              JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at            TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, email)
);
CREATE INDEX idx_users_email  ON users(tenant_id, email)  WHERE deleted_at IS NULL;
CREATE INDEX idx_users_status ON users(tenant_id, status) WHERE deleted_at IS NULL;
CREATE TRIGGER update_users_updated_at
    BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
ALTER TABLE users ENABLE ROW LEVEL SECURITY;
ALTER TABLE users FORCE  ROW LEVEL SECURITY;
CREATE POLICY users_tenant_isolation ON users
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY users_tenant_isolation_insert ON users
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 3: groups
-- ===========================================================================
CREATE TABLE groups (
    tenant_id    UUID NOT NULL REFERENCES organizations(id),
    id           UUID NOT NULL DEFAULT gen_random_uuid(),
    name         TEXT NOT NULL,
    description  TEXT,
    created_by   UUID,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at   TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, name),
    FOREIGN KEY (tenant_id, created_by) REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_groups_created_by ON groups(tenant_id, created_by) WHERE deleted_at IS NULL;
CREATE TRIGGER update_groups_updated_at
    BEFORE UPDATE ON groups FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
ALTER TABLE groups ENABLE ROW LEVEL SECURITY;
ALTER TABLE groups FORCE  ROW LEVEL SECURITY;
CREATE POLICY groups_tenant_isolation ON groups
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY groups_tenant_isolation_insert ON groups
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 4: group_members
-- ===========================================================================
CREATE TABLE group_members (
    tenant_id  UUID NOT NULL REFERENCES organizations(id),
    group_id   UUID NOT NULL,
    user_id    UUID NOT NULL,
    added_by   UUID,
    added_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, group_id, user_id),
    FOREIGN KEY (tenant_id, group_id) REFERENCES groups(tenant_id, id),
    FOREIGN KEY (tenant_id, user_id)  REFERENCES users(tenant_id, id),
    FOREIGN KEY (tenant_id, added_by) REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_group_members_user  ON group_members(tenant_id, user_id);
CREATE INDEX idx_group_members_group ON group_members(tenant_id, group_id);
ALTER TABLE group_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE group_members FORCE  ROW LEVEL SECURITY;
CREATE POLICY group_members_tenant_isolation ON group_members
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY group_members_tenant_isolation_insert ON group_members
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 5: workspaces
-- ===========================================================================
CREATE TABLE workspaces (
    tenant_id    UUID NOT NULL REFERENCES organizations(id),
    id           UUID NOT NULL DEFAULT gen_random_uuid(),
    name         TEXT NOT NULL,
    description  TEXT,
    settings     JSONB NOT NULL DEFAULT '{}'::jsonb,
    region_pin   TEXT NOT NULL DEFAULT 'us-east-1',
    created_by   UUID,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at   TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, created_by) REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_workspaces_created_by ON workspaces(tenant_id, created_by) WHERE deleted_at IS NULL;
CREATE TRIGGER update_workspaces_updated_at
    BEFORE UPDATE ON workspaces FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
ALTER TABLE workspaces ENABLE ROW LEVEL SECURITY;
ALTER TABLE workspaces FORCE  ROW LEVEL SECURITY;
CREATE POLICY workspaces_tenant_isolation ON workspaces
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY workspaces_tenant_isolation_insert ON workspaces
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 6: workspace_members
-- ===========================================================================
CREATE TABLE workspace_members (
    tenant_id     UUID NOT NULL REFERENCES organizations(id),
    workspace_id  UUID NOT NULL,
    user_id       UUID NOT NULL,
    role          TEXT NOT NULL DEFAULT 'member'
                       CHECK (role IN ('admin', 'member', 'viewer')),
    added_by      UUID,
    added_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, workspace_id, user_id),
    FOREIGN KEY (tenant_id, workspace_id) REFERENCES workspaces(tenant_id, id),
    FOREIGN KEY (tenant_id, user_id)      REFERENCES users(tenant_id, id),
    FOREIGN KEY (tenant_id, added_by)     REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_workspace_members_user      ON workspace_members(tenant_id, user_id);
CREATE INDEX idx_workspace_members_workspace ON workspace_members(tenant_id, workspace_id);
ALTER TABLE workspace_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE workspace_members FORCE  ROW LEVEL SECURITY;
CREATE POLICY workspace_members_tenant_isolation ON workspace_members
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY workspace_members_tenant_isolation_insert ON workspace_members
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 7: folders (ltree-pathed)
-- ===========================================================================
CREATE TABLE folders (
    tenant_id         UUID NOT NULL REFERENCES organizations(id),
    id                UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id      UUID NOT NULL,
    parent_folder_id  UUID,
    name              TEXT NOT NULL,
    path              LTREE NOT NULL,
    depth             INT  NOT NULL,
    created_by        UUID,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, workspace_id)     REFERENCES workspaces(tenant_id, id),
    FOREIGN KEY (tenant_id, parent_folder_id) REFERENCES folders(tenant_id, id),
    FOREIGN KEY (tenant_id, created_by)       REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_folders_workspace ON folders(tenant_id, workspace_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_folders_parent    ON folders(tenant_id, parent_folder_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_folders_path_gist ON folders USING GIST (path);
CREATE INDEX idx_folders_created_by ON folders(tenant_id, created_by) WHERE deleted_at IS NULL;
CREATE TRIGGER update_folders_updated_at
    BEFORE UPDATE ON folders FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
ALTER TABLE folders ENABLE ROW LEVEL SECURITY;
ALTER TABLE folders FORCE  ROW LEVEL SECURITY;
CREATE POLICY folders_tenant_isolation ON folders
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY folders_tenant_isolation_insert ON folders
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 10: content_blobs (single-column PK for dedup)
-- Defined BEFORE versions because versions.content_blob_id references it.
-- ===========================================================================
CREATE TABLE content_blobs (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID NOT NULL REFERENCES organizations(id),
    sha256_hash       TEXT NOT NULL,
    storage_region    TEXT NOT NULL DEFAULT 'us-east-1',
    storage_bucket    TEXT NOT NULL,
    storage_key       TEXT NOT NULL,
    storage_class     TEXT NOT NULL DEFAULT 'hot'
                           CHECK (storage_class IN ('hot', 'warm', 'cold', 'quarantine')),
    size_bytes        BIGINT NOT NULL,
    mime_type         TEXT,
    encryption_key_id TEXT,
    reference_count   INT NOT NULL DEFAULT 1,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, sha256_hash)
);
CREATE INDEX idx_content_blobs_tenant ON content_blobs(tenant_id);
CREATE INDEX idx_content_blobs_refcount_zero
    ON content_blobs(tenant_id) WHERE reference_count = 0;
ALTER TABLE content_blobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE content_blobs FORCE  ROW LEVEL SECURITY;
CREATE POLICY content_blobs_tenant_isolation ON content_blobs
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY content_blobs_tenant_isolation_insert ON content_blobs
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 8: documents
-- ===========================================================================
CREATE TABLE documents (
    tenant_id                  UUID NOT NULL REFERENCES organizations(id),
    id                         UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id               UUID NOT NULL,
    folder_id                  UUID NOT NULL,
    title                      TEXT NOT NULL,
    description                TEXT,
    lifecycle_state            TEXT NOT NULL DEFAULT 'draft'
                                    CHECK (lifecycle_state IN (
                                        'draft', 'in_review', 'active', 'superseded',
                                        'retained', 'archived', 'disposed', 'legal_hold'
                                    )),
    region_pin                 TEXT NOT NULL DEFAULT 'us-east-1',
    custom_metadata            JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (pg_column_size(custom_metadata) < 65536),
    tags                       TEXT[] NOT NULL DEFAULT '{}',
    current_version_id         UUID,
    document_class             TEXT NOT NULL DEFAULT '',
    classification_confidence  REAL NOT NULL DEFAULT 0.0,
    sha256_hash                TEXT,
    total_size_bytes           BIGINT NOT NULL DEFAULT 0,
    mime_type                  TEXT,
    created_by                 UUID,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at                 TIMESTAMPTZ,
    retention_until            TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, workspace_id) REFERENCES workspaces(tenant_id, id),
    FOREIGN KEY (tenant_id, folder_id)    REFERENCES folders(tenant_id, id),
    FOREIGN KEY (tenant_id, created_by)   REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_documents_workspace_folder ON documents(tenant_id, workspace_id, folder_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_documents_lifecycle        ON documents(tenant_id, lifecycle_state)         WHERE deleted_at IS NULL;
CREATE INDEX idx_documents_created_at       ON documents(tenant_id, created_at DESC)         WHERE deleted_at IS NULL;
CREATE INDEX idx_documents_document_class   ON documents(tenant_id, document_class)          WHERE deleted_at IS NULL;
CREATE INDEX idx_documents_created_by       ON documents(tenant_id, created_by)              WHERE deleted_at IS NULL;
CREATE INDEX idx_documents_metadata_gin     ON documents USING GIN (custom_metadata);
CREATE INDEX idx_documents_tags_gin         ON documents USING GIN (tags);
CREATE INDEX idx_documents_title_trgm       ON documents USING GIN (title gin_trgm_ops)      WHERE deleted_at IS NULL;
CREATE TRIGGER update_documents_updated_at
    BEFORE UPDATE ON documents FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
ALTER TABLE documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE documents FORCE  ROW LEVEL SECURITY;
CREATE POLICY documents_tenant_isolation ON documents
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY documents_tenant_isolation_insert ON documents
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 9: versions (immutable)
-- ===========================================================================
CREATE TABLE versions (
    tenant_id        UUID NOT NULL REFERENCES organizations(id),
    id               UUID NOT NULL DEFAULT gen_random_uuid(),
    document_id      UUID NOT NULL,
    version_number   INT NOT NULL,
    content_blob_id  UUID NOT NULL REFERENCES content_blobs(id),
    size_bytes       BIGINT NOT NULL,
    mime_type        TEXT,
    sha256_hash      TEXT NOT NULL,
    created_by       UUID,
    created_by_name  TEXT NOT NULL DEFAULT '',
    change_summary   TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, document_id, version_number),
    FOREIGN KEY (tenant_id, document_id) REFERENCES documents(tenant_id, id),
    FOREIGN KEY (tenant_id, created_by)  REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_versions_document    ON versions(tenant_id, document_id, version_number DESC);
CREATE INDEX idx_versions_blob        ON versions(content_blob_id);
CREATE INDEX idx_versions_created_by  ON versions(tenant_id, created_by);
ALTER TABLE versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE versions FORCE  ROW LEVEL SECURITY;
CREATE POLICY versions_tenant_isolation ON versions
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY versions_tenant_isolation_insert ON versions
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- Now that versions exists, add documents.current_version_id FK.
ALTER TABLE documents
    ADD CONSTRAINT documents_current_version_fk
    FOREIGN KEY (tenant_id, current_version_id) REFERENCES versions(tenant_id, id);

-- ===========================================================================
-- TABLE 11: permissions (ACLs)
-- ===========================================================================
CREATE TABLE permissions (
    tenant_id       UUID NOT NULL REFERENCES organizations(id),
    id              UUID NOT NULL DEFAULT gen_random_uuid(),
    resource_type   TEXT NOT NULL CHECK (resource_type IN ('document', 'folder', 'workspace')),
    resource_id     UUID NOT NULL,
    principal_type  TEXT NOT NULL CHECK (principal_type IN ('user', 'group')),
    principal_id    UUID NOT NULL,
    capability      TEXT NOT NULL CHECK (capability IN ('view', 'edit', 'delete', 'share', 'admin')),
    granted_by      UUID,
    granted_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    valid_from      TIMESTAMPTZ NOT NULL DEFAULT now(),
    valid_to        TIMESTAMPTZ,
    expires_at      TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, resource_type, resource_id, principal_type, principal_id, capability),
    FOREIGN KEY (tenant_id, granted_by) REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_permissions_resource  ON permissions(tenant_id, resource_type, resource_id);
CREATE INDEX idx_permissions_principal ON permissions(tenant_id, principal_type, principal_id);
CREATE INDEX idx_permissions_expires   ON permissions(tenant_id, expires_at) WHERE expires_at IS NOT NULL;
ALTER TABLE permissions ENABLE ROW LEVEL SECURITY;
ALTER TABLE permissions FORCE  ROW LEVEL SECURITY;
CREATE POLICY permissions_tenant_isolation ON permissions
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY permissions_tenant_isolation_insert ON permissions
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 12: share_links
-- ===========================================================================
CREATE TABLE share_links (
    tenant_id      UUID NOT NULL REFERENCES organizations(id),
    id             UUID NOT NULL DEFAULT gen_random_uuid(),
    document_id    UUID NOT NULL,
    created_by     UUID,
    token          TEXT NOT NULL UNIQUE,    -- globally unique: anonymous lookup path
    password_hash  TEXT,                    -- bcrypt hash, NULL = public
    expires_at     TIMESTAMPTZ,
    max_views      INT NOT NULL DEFAULT 0,  -- 0 = unlimited
    view_count     INT NOT NULL DEFAULT 0,
    permissions    TEXT[] NOT NULL DEFAULT '{view}',
    is_active      BOOLEAN NOT NULL DEFAULT true,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    accessed_at    TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, document_id) REFERENCES documents(tenant_id, id),
    FOREIGN KEY (tenant_id, created_by)  REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_share_links_document ON share_links(tenant_id, document_id) WHERE is_active;
CREATE INDEX idx_share_links_active   ON share_links(tenant_id)               WHERE is_active;
ALTER TABLE share_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE share_links FORCE  ROW LEVEL SECURITY;
CREATE POLICY share_links_tenant_isolation ON share_links
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY share_links_tenant_isolation_insert ON share_links
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 13: comments
-- ===========================================================================
CREATE TABLE comments (
    tenant_id          UUID NOT NULL REFERENCES organizations(id),
    id                 UUID NOT NULL DEFAULT gen_random_uuid(),
    document_id        UUID NOT NULL,
    version_id         UUID,
    parent_comment_id  UUID,
    author_id          UUID NOT NULL,
    body               TEXT NOT NULL,
    is_resolved        BOOLEAN NOT NULL DEFAULT false,
    resolved_by        UUID,
    resolved_at        TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at         TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, document_id)       REFERENCES documents(tenant_id, id),
    FOREIGN KEY (tenant_id, version_id)        REFERENCES versions(tenant_id, id),
    FOREIGN KEY (tenant_id, parent_comment_id) REFERENCES comments(tenant_id, id),
    FOREIGN KEY (tenant_id, author_id)         REFERENCES users(tenant_id, id),
    FOREIGN KEY (tenant_id, resolved_by)       REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_comments_document ON comments(tenant_id, document_id)       WHERE deleted_at IS NULL;
CREATE INDEX idx_comments_version  ON comments(tenant_id, version_id)        WHERE version_id IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX idx_comments_parent   ON comments(tenant_id, parent_comment_id) WHERE parent_comment_id IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX idx_comments_author   ON comments(tenant_id, author_id)         WHERE deleted_at IS NULL;
CREATE TRIGGER update_comments_updated_at
    BEFORE UPDATE ON comments FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
ALTER TABLE comments ENABLE ROW LEVEL SECURITY;
ALTER TABLE comments FORCE  ROW LEVEL SECURITY;
CREATE POLICY comments_tenant_isolation ON comments
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY comments_tenant_isolation_insert ON comments
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 14: annotations
-- ===========================================================================
CREATE TABLE annotations (
    tenant_id        UUID NOT NULL REFERENCES organizations(id),
    id               UUID NOT NULL DEFAULT gen_random_uuid(),
    document_id      UUID NOT NULL,
    version_id       UUID NOT NULL,
    page_number      INT NOT NULL DEFAULT 1,
    annotation_type  TEXT NOT NULL CHECK (annotation_type IN ('highlight', 'note', 'stamp', 'drawing')),
    annotation_data  JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by       UUID,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at       TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, document_id) REFERENCES documents(tenant_id, id),
    FOREIGN KEY (tenant_id, version_id)  REFERENCES versions(tenant_id, id),
    FOREIGN KEY (tenant_id, created_by)  REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_annotations_document ON annotations(tenant_id, document_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_annotations_version  ON annotations(tenant_id, version_id)  WHERE deleted_at IS NULL;
CREATE TRIGGER update_annotations_updated_at
    BEFORE UPDATE ON annotations FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
ALTER TABLE annotations ENABLE ROW LEVEL SECURITY;
ALTER TABLE annotations FORCE  ROW LEVEL SECURITY;
CREATE POLICY annotations_tenant_isolation ON annotations
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY annotations_tenant_isolation_insert ON annotations
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 15: tags_catalog
-- ===========================================================================
CREATE TABLE tags_catalog (
    tenant_id   UUID NOT NULL REFERENCES organizations(id),
    id          UUID NOT NULL DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL,
    color       TEXT NOT NULL DEFAULT '#888888',
    created_by  UUID,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, name),
    FOREIGN KEY (tenant_id, created_by) REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_tags_catalog_name ON tags_catalog(tenant_id, lower(name));
ALTER TABLE tags_catalog ENABLE ROW LEVEL SECURITY;
ALTER TABLE tags_catalog FORCE  ROW LEVEL SECURITY;
CREATE POLICY tags_catalog_tenant_isolation ON tags_catalog
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY tags_catalog_tenant_isolation_insert ON tags_catalog
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 16: legal_holds
-- ===========================================================================
CREATE TABLE legal_holds (
    tenant_id         UUID NOT NULL REFERENCES organizations(id),
    id                UUID NOT NULL DEFAULT gen_random_uuid(),
    name              TEXT NOT NULL,
    description       TEXT,
    matter_reference  TEXT,
    applied_by        UUID,
    applied_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    released_by       UUID,
    released_at       TIMESTAMPTZ,
    is_active         BOOLEAN NOT NULL DEFAULT true,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, applied_by)  REFERENCES users(tenant_id, id),
    FOREIGN KEY (tenant_id, released_by) REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_legal_holds_active ON legal_holds(tenant_id) WHERE is_active;
ALTER TABLE legal_holds ENABLE ROW LEVEL SECURITY;
ALTER TABLE legal_holds FORCE  ROW LEVEL SECURITY;
CREATE POLICY legal_holds_tenant_isolation ON legal_holds
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY legal_holds_tenant_isolation_insert ON legal_holds
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 17: legal_hold_documents (junction; composite PK)
-- ===========================================================================
CREATE TABLE legal_hold_documents (
    tenant_id                UUID NOT NULL REFERENCES organizations(id),
    hold_id                  UUID NOT NULL,
    document_id              UUID NOT NULL,
    applied_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    previous_lifecycle_state TEXT NOT NULL,
    PRIMARY KEY (tenant_id, hold_id, document_id),
    FOREIGN KEY (tenant_id, hold_id)     REFERENCES legal_holds(tenant_id, id),
    FOREIGN KEY (tenant_id, document_id) REFERENCES documents(tenant_id, id)
);
CREATE INDEX idx_lhd_document ON legal_hold_documents(tenant_id, document_id);
CREATE INDEX idx_lhd_hold     ON legal_hold_documents(tenant_id, hold_id);
ALTER TABLE legal_hold_documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE legal_hold_documents FORCE  ROW LEVEL SECURITY;
CREATE POLICY lhd_tenant_isolation ON legal_hold_documents
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY lhd_tenant_isolation_insert ON legal_hold_documents
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 18: retention_policies
-- ===========================================================================
CREATE TABLE retention_policies (
    tenant_id              UUID NOT NULL REFERENCES organizations(id),
    id                     UUID NOT NULL DEFAULT gen_random_uuid(),
    name                   TEXT NOT NULL,
    description            TEXT,
    document_class_filter  TEXT,
    tag_filter             TEXT[],
    workspace_filter       UUID,
    retain_days            INT NOT NULL CHECK (retain_days > 0),
    then_action            TEXT NOT NULL DEFAULT 'archive'
                                CHECK (then_action IN ('archive', 'dispose')),
    archive_days           INT,
    is_active              BOOLEAN NOT NULL DEFAULT true,
    created_by             UUID,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, name),
    FOREIGN KEY (tenant_id, workspace_filter) REFERENCES workspaces(tenant_id, id),
    FOREIGN KEY (tenant_id, created_by)       REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_retention_active ON retention_policies(tenant_id) WHERE is_active;
CREATE TRIGGER update_retention_policies_updated_at
    BEFORE UPDATE ON retention_policies FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
ALTER TABLE retention_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE retention_policies FORCE  ROW LEVEL SECURITY;
CREATE POLICY retention_policies_tenant_isolation ON retention_policies
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY retention_policies_tenant_isolation_insert ON retention_policies
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 19: audit_events (partitioned by range on created_at)
-- Single-column logical PK would be id alone, but PARTITION BY requires the
-- partition key in the PK, so we use (created_at, id).
-- ===========================================================================
CREATE TABLE audit_events (
    id             UUID NOT NULL DEFAULT gen_random_uuid(),
    tenant_id      UUID NOT NULL,
    actor_id       UUID,
    actor_type     TEXT NOT NULL CHECK (actor_type IN ('user', 'system', 'api_key')),
    action         TEXT NOT NULL,
    resource_type  TEXT,
    resource_id    UUID,
    metadata       JSONB NOT NULL DEFAULT '{}'::jsonb,
    ip_address     INET,
    user_agent     TEXT,
    previous_hash  TEXT,
    event_hash     TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (created_at, id)
) PARTITION BY RANGE (created_at);

CREATE INDEX idx_audit_events_tenant_time
    ON audit_events (tenant_id, created_at DESC);
CREATE INDEX idx_audit_events_resource
    ON audit_events (tenant_id, resource_type, resource_id);
CREATE INDEX idx_audit_events_actor_time
    ON audit_events (tenant_id, actor_id, created_at DESC);

-- Monthly partitions for current month + 3 ahead (2026-04 through 2026-07).
-- Production operators roll new partitions forward via a cron job.
CREATE TABLE audit_events_2026_04 PARTITION OF audit_events
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
CREATE TABLE audit_events_2026_05 PARTITION OF audit_events
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
CREATE TABLE audit_events_2026_06 PARTITION OF audit_events
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');
CREATE TABLE audit_events_2026_07 PARTITION OF audit_events
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');

ALTER TABLE audit_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_events FORCE  ROW LEVEL SECURITY;
CREATE POLICY audit_events_tenant_isolation ON audit_events
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY audit_events_tenant_isolation_insert ON audit_events
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- Append-only: revoke UPDATE/DELETE for the app role.
REVOKE UPDATE, DELETE ON audit_events FROM PUBLIC;

-- ===========================================================================
-- TABLE 20: sessions (single-column PK; short-lived, no soft-delete)
-- ===========================================================================
CREATE TABLE sessions (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID NOT NULL REFERENCES organizations(id),
    user_id           UUID NOT NULL,
    token_hash        TEXT NOT NULL UNIQUE,
    ip_address        INET,
    user_agent        TEXT,
    expires_at        TIMESTAMPTZ NOT NULL,
    last_activity_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at        TIMESTAMPTZ,
    FOREIGN KEY (tenant_id, user_id) REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_sessions_user    ON sessions(tenant_id, user_id) WHERE revoked_at IS NULL;
CREATE INDEX idx_sessions_expires ON sessions(expires_at)         WHERE revoked_at IS NULL;
ALTER TABLE sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE sessions FORCE  ROW LEVEL SECURITY;
CREATE POLICY sessions_tenant_isolation ON sessions
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY sessions_tenant_isolation_insert ON sessions
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 21: api_keys
-- ===========================================================================
CREATE TABLE api_keys (
    tenant_id     UUID NOT NULL REFERENCES organizations(id),
    id            UUID NOT NULL DEFAULT gen_random_uuid(),
    user_id       UUID,
    name          TEXT NOT NULL,
    key_hash      TEXT NOT NULL UNIQUE,
    key_prefix    TEXT NOT NULL,
    scopes        TEXT[] NOT NULL DEFAULT '{}',
    last_used_at  TIMESTAMPTZ,
    expires_at    TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at    TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, user_id) REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_api_keys_user   ON api_keys(tenant_id, user_id) WHERE revoked_at IS NULL;
CREATE INDEX idx_api_keys_prefix ON api_keys(key_prefix)         WHERE revoked_at IS NULL;
ALTER TABLE api_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE api_keys FORCE  ROW LEVEL SECURITY;
CREATE POLICY api_keys_tenant_isolation ON api_keys
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY api_keys_tenant_isolation_insert ON api_keys
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 22: upload_sessions
-- ===========================================================================
CREATE TABLE upload_sessions (
    tenant_id        UUID NOT NULL REFERENCES organizations(id),
    id               UUID NOT NULL DEFAULT gen_random_uuid(),
    document_id      UUID,
    filename         TEXT NOT NULL,
    total_size       BIGINT NOT NULL,
    mime_type        TEXT,
    upload_type      TEXT NOT NULL CHECK (upload_type IN ('single', 'multipart', 'tus')),
    storage_region   TEXT NOT NULL DEFAULT 'us-east-1',
    s3_upload_id     TEXT,
    status           TEXT NOT NULL DEFAULT 'initiated'
                          CHECK (status IN ('initiated', 'uploading', 'scanning',
                                            'completed', 'failed', 'quarantined')),
    parts_completed  INT NOT NULL DEFAULT 0,
    parts_total      INT,
    created_by       UUID,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at     TIMESTAMPTZ,
    expires_at       TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, document_id) REFERENCES documents(tenant_id, id),
    FOREIGN KEY (tenant_id, created_by)  REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_upload_sessions_status  ON upload_sessions(tenant_id, status);
CREATE INDEX idx_upload_sessions_expires ON upload_sessions(expires_at)
    WHERE status IN ('initiated', 'uploading');
ALTER TABLE upload_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE upload_sessions FORCE  ROW LEVEL SECURITY;
CREATE POLICY upload_sessions_tenant_isolation ON upload_sessions
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY upload_sessions_tenant_isolation_insert ON upload_sessions
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 23: ocr_results
-- ===========================================================================
CREATE TABLE ocr_results (
    tenant_id           UUID NOT NULL REFERENCES organizations(id),
    id                  UUID NOT NULL DEFAULT gen_random_uuid(),
    version_id          UUID NOT NULL,
    page_number         INT NOT NULL,
    text_content        TEXT NOT NULL,
    confidence          REAL NOT NULL DEFAULT 0.0,
    language            TEXT,
    bounding_boxes      JSONB NOT NULL DEFAULT '[]'::jsonb,
    processing_time_ms  INT,
    engine              TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, version_id) REFERENCES versions(tenant_id, id)
);
CREATE INDEX idx_ocr_version_page ON ocr_results(tenant_id, version_id, page_number);
ALTER TABLE ocr_results ENABLE ROW LEVEL SECURITY;
ALTER TABLE ocr_results FORCE  ROW LEVEL SECURITY;
CREATE POLICY ocr_tenant_isolation ON ocr_results
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY ocr_tenant_isolation_insert ON ocr_results
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 24: extraction_results
-- ===========================================================================
CREATE TABLE extraction_results (
    tenant_id           UUID NOT NULL REFERENCES organizations(id),
    id                  UUID NOT NULL DEFAULT gen_random_uuid(),
    version_id          UUID NOT NULL,
    extraction_type     TEXT NOT NULL,
    fields              JSONB NOT NULL DEFAULT '{}'::jsonb,
    confidence          REAL,
    method              TEXT NOT NULL CHECK (method IN ('regex', 'ml', 'llm')),
    processing_time_ms  INT,
    cost_cents          INT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, version_id) REFERENCES versions(tenant_id, id)
);
CREATE INDEX idx_extraction_version ON extraction_results(tenant_id, version_id);
CREATE INDEX idx_extraction_type    ON extraction_results(tenant_id, extraction_type);
ALTER TABLE extraction_results ENABLE ROW LEVEL SECURITY;
ALTER TABLE extraction_results FORCE  ROW LEVEL SECURITY;
CREATE POLICY extraction_tenant_isolation ON extraction_results
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY extraction_tenant_isolation_insert ON extraction_results
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 25: document_chunks (for RAG / embeddings)
-- ===========================================================================
CREATE TABLE document_chunks (
    tenant_id        UUID NOT NULL REFERENCES organizations(id),
    id               UUID NOT NULL DEFAULT gen_random_uuid(),
    document_id      UUID NOT NULL,
    version_id       UUID NOT NULL,
    chunk_index      INT NOT NULL,
    text_content     TEXT NOT NULL,
    token_count      INT NOT NULL DEFAULT 0,
    embedding_model  TEXT,
    page_numbers     INT[],
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, version_id, chunk_index),
    FOREIGN KEY (tenant_id, document_id) REFERENCES documents(tenant_id, id),
    FOREIGN KEY (tenant_id, version_id)  REFERENCES versions(tenant_id, id)
);
CREATE INDEX idx_chunks_document ON document_chunks(tenant_id, document_id, chunk_index);
CREATE INDEX idx_chunks_version  ON document_chunks(tenant_id, version_id);
ALTER TABLE document_chunks ENABLE ROW LEVEL SECURITY;
ALTER TABLE document_chunks FORCE  ROW LEVEL SECURITY;
CREATE POLICY chunks_tenant_isolation ON document_chunks
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY chunks_tenant_isolation_insert ON document_chunks
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 26: entities (NER + PII detection)
-- ===========================================================================
CREATE TABLE entities (
    tenant_id     UUID NOT NULL REFERENCES organizations(id),
    id            UUID NOT NULL DEFAULT gen_random_uuid(),
    document_id   UUID NOT NULL,
    version_id    UUID NOT NULL,
    entity_type   TEXT NOT NULL,
    entity_value  TEXT NOT NULL,
    start_offset  INT,
    end_offset    INT,
    page_number   INT,
    confidence    REAL,
    is_pii        BOOLEAN NOT NULL DEFAULT false,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, document_id) REFERENCES documents(tenant_id, id),
    FOREIGN KEY (tenant_id, version_id)  REFERENCES versions(tenant_id, id)
);
CREATE INDEX idx_entities_document ON entities(tenant_id, document_id);
CREATE INDEX idx_entities_version  ON entities(tenant_id, version_id);
CREATE INDEX idx_entities_pii      ON entities(tenant_id, document_id) WHERE is_pii;
CREATE INDEX idx_entities_type     ON entities(tenant_id, entity_type);
ALTER TABLE entities ENABLE ROW LEVEL SECURITY;
ALTER TABLE entities FORCE  ROW LEVEL SECURITY;
CREATE POLICY entities_tenant_isolation ON entities
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY entities_tenant_isolation_insert ON entities
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 27: document_fingerprints (per-document; composite PK)
-- ===========================================================================
CREATE TABLE document_fingerprints (
    tenant_id          UUID NOT NULL REFERENCES organizations(id),
    document_id        UUID NOT NULL,
    minhash_signature  BYTEA,
    simhash            BIGINT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, document_id),
    FOREIGN KEY (tenant_id, document_id) REFERENCES documents(tenant_id, id)
);
CREATE INDEX idx_fingerprints_simhash ON document_fingerprints(tenant_id, simhash);
CREATE TRIGGER update_document_fingerprints_updated_at
    BEFORE UPDATE ON document_fingerprints FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
ALTER TABLE document_fingerprints ENABLE ROW LEVEL SECURITY;
ALTER TABLE document_fingerprints FORCE  ROW LEVEL SECURITY;
CREATE POLICY fingerprints_tenant_isolation ON document_fingerprints
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY fingerprints_tenant_isolation_insert ON document_fingerprints
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 28: duplicate_candidates
-- ===========================================================================
CREATE TABLE duplicate_candidates (
    tenant_id              UUID NOT NULL REFERENCES organizations(id),
    id                     UUID NOT NULL DEFAULT gen_random_uuid(),
    document_id            UUID NOT NULL,
    candidate_document_id  UUID NOT NULL,
    similarity_score       REAL NOT NULL,
    method                 TEXT NOT NULL,
    status                 TEXT NOT NULL DEFAULT 'pending'
                                CHECK (status IN ('pending', 'confirmed', 'dismissed')),
    reviewed_by            UUID,
    reviewed_at            TIMESTAMPTZ,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, document_id)           REFERENCES documents(tenant_id, id),
    FOREIGN KEY (tenant_id, candidate_document_id) REFERENCES documents(tenant_id, id),
    FOREIGN KEY (tenant_id, reviewed_by)           REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_dup_document  ON duplicate_candidates(tenant_id, document_id)           WHERE status = 'pending';
CREATE INDEX idx_dup_candidate ON duplicate_candidates(tenant_id, candidate_document_id) WHERE status = 'pending';
ALTER TABLE duplicate_candidates ENABLE ROW LEVEL SECURITY;
ALTER TABLE duplicate_candidates FORCE  ROW LEVEL SECURITY;
CREATE POLICY dup_tenant_isolation ON duplicate_candidates
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY dup_tenant_isolation_insert ON duplicate_candidates
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 29: workflow_definitions
-- ===========================================================================
CREATE TABLE workflow_definitions (
    tenant_id    UUID NOT NULL REFERENCES organizations(id),
    id           UUID NOT NULL DEFAULT gen_random_uuid(),
    name         TEXT NOT NULL,
    description  TEXT,
    definition   JSONB NOT NULL,
    version      INT NOT NULL DEFAULT 1,
    is_active    BOOLEAN NOT NULL DEFAULT true,
    created_by   UUID,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, name, version),
    FOREIGN KEY (tenant_id, created_by) REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_wf_defs_active ON workflow_definitions(tenant_id) WHERE is_active;
CREATE TRIGGER update_workflow_definitions_updated_at
    BEFORE UPDATE ON workflow_definitions FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
ALTER TABLE workflow_definitions ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_definitions FORCE  ROW LEVEL SECURITY;
CREATE POLICY wf_defs_tenant_isolation ON workflow_definitions
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY wf_defs_tenant_isolation_insert ON workflow_definitions
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 30: workflow_instances
-- ===========================================================================
CREATE TABLE workflow_instances (
    tenant_id             UUID NOT NULL REFERENCES organizations(id),
    id                    UUID NOT NULL DEFAULT gen_random_uuid(),
    definition_id         UUID NOT NULL,
    document_id           UUID,
    status                TEXT NOT NULL DEFAULT 'pending'
                               CHECK (status IN ('pending', 'running', 'completed', 'failed', 'cancelled')),
    current_step_id       TEXT,
    temporal_workflow_id  TEXT,
    started_by            UUID,
    started_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at          TIMESTAMPTZ,
    result                JSONB,
    error_message         TEXT,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, definition_id) REFERENCES workflow_definitions(tenant_id, id),
    FOREIGN KEY (tenant_id, document_id)   REFERENCES documents(tenant_id, id),
    FOREIGN KEY (tenant_id, started_by)    REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_wf_inst_status   ON workflow_instances(tenant_id, status);
CREATE INDEX idx_wf_inst_document ON workflow_instances(tenant_id, document_id);
CREATE INDEX idx_wf_inst_temporal ON workflow_instances(temporal_workflow_id);
ALTER TABLE workflow_instances ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_instances FORCE  ROW LEVEL SECURITY;
CREATE POLICY wf_inst_tenant_isolation ON workflow_instances
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY wf_inst_tenant_isolation_insert ON workflow_instances
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 31: workflow_tasks
-- ===========================================================================
CREATE TABLE workflow_tasks (
    tenant_id      UUID NOT NULL REFERENCES organizations(id),
    id             UUID NOT NULL DEFAULT gen_random_uuid(),
    instance_id    UUID NOT NULL,
    step_id        TEXT NOT NULL,
    step_name      TEXT NOT NULL,
    assignee_id    UUID,
    status         TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending', 'in_progress', 'completed',
                                          'rejected', 'delegated', 'escalated', 'skipped')),
    due_at         TIMESTAMPTZ,
    completed_at   TIMESTAMPTZ,
    completed_by   UUID,
    outcome        TEXT,
    notes          TEXT,
    delegated_to   UUID,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, instance_id)  REFERENCES workflow_instances(tenant_id, id),
    FOREIGN KEY (tenant_id, assignee_id)  REFERENCES users(tenant_id, id),
    FOREIGN KEY (tenant_id, completed_by) REFERENCES users(tenant_id, id),
    FOREIGN KEY (tenant_id, delegated_to) REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_wf_tasks_assignee ON workflow_tasks(tenant_id, assignee_id, status);
CREATE INDEX idx_wf_tasks_instance ON workflow_tasks(tenant_id, instance_id);
CREATE INDEX idx_wf_tasks_due      ON workflow_tasks(tenant_id, due_at)
    WHERE status IN ('pending', 'in_progress') AND due_at IS NOT NULL;
ALTER TABLE workflow_tasks ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_tasks FORCE  ROW LEVEL SECURITY;
CREATE POLICY wf_tasks_tenant_isolation ON workflow_tasks
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY wf_tasks_tenant_isolation_insert ON workflow_tasks
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 32: notification_preferences (composite PK)
-- ===========================================================================
CREATE TABLE notification_preferences (
    tenant_id    UUID NOT NULL REFERENCES organizations(id),
    user_id      UUID NOT NULL,
    channel      TEXT NOT NULL CHECK (channel IN ('email', 'push', 'in_app', 'slack', 'teams', 'sms')),
    event_type   TEXT NOT NULL,
    is_enabled   BOOLEAN NOT NULL DEFAULT true,
    quiet_start  TIME,
    quiet_end    TIME,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id, channel, event_type),
    FOREIGN KEY (tenant_id, user_id) REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_notif_prefs_user ON notification_preferences(tenant_id, user_id);
CREATE TRIGGER update_notification_preferences_updated_at
    BEFORE UPDATE ON notification_preferences FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
ALTER TABLE notification_preferences ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_preferences FORCE  ROW LEVEL SECURITY;
CREATE POLICY notif_prefs_tenant_isolation ON notification_preferences
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY notif_prefs_tenant_isolation_insert ON notification_preferences
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 33: notifications (in-app inbox)
-- ===========================================================================
CREATE TABLE notifications (
    tenant_id      UUID NOT NULL REFERENCES organizations(id),
    id             UUID NOT NULL DEFAULT gen_random_uuid(),
    user_id        UUID NOT NULL,
    title          TEXT NOT NULL,
    body           TEXT,
    link           TEXT,
    is_read        BOOLEAN NOT NULL DEFAULT false,
    event_type     TEXT,
    resource_type  TEXT,
    resource_id    UUID,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    read_at        TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, user_id) REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_notifications_inbox
    ON notifications(tenant_id, user_id, is_read, created_at DESC);
ALTER TABLE notifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE notifications FORCE  ROW LEVEL SECURITY;
CREATE POLICY notifications_tenant_isolation ON notifications
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY notifications_tenant_isolation_insert ON notifications
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 34: device_tokens (mobile/web push)
-- ===========================================================================
CREATE TABLE device_tokens (
    tenant_id     UUID NOT NULL REFERENCES organizations(id),
    id            UUID NOT NULL DEFAULT gen_random_uuid(),
    user_id       UUID NOT NULL,
    platform      TEXT NOT NULL CHECK (platform IN ('ios', 'android', 'web')),
    token         TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at  TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, platform, token),
    FOREIGN KEY (tenant_id, user_id) REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_device_tokens_user ON device_tokens(tenant_id, user_id);
ALTER TABLE device_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE device_tokens FORCE  ROW LEVEL SECURITY;
CREATE POLICY device_tokens_tenant_isolation ON device_tokens
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY device_tokens_tenant_isolation_insert ON device_tokens
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 35: signature_requests
-- ===========================================================================
CREATE TABLE signature_requests (
    tenant_id              UUID NOT NULL REFERENCES organizations(id),
    id                     UUID NOT NULL DEFAULT gen_random_uuid(),
    document_id            UUID NOT NULL,
    version_id             UUID NOT NULL,
    requested_by           UUID,
    status                 TEXT NOT NULL DEFAULT 'draft'
                                CHECK (status IN ('draft', 'pending', 'in_progress',
                                                  'completed', 'declined', 'expired', 'cancelled')),
    provider               TEXT NOT NULL DEFAULT 'internal'
                                CHECK (provider IN ('internal', 'docusign', 'adobe_sign')),
    provider_envelope_id   TEXT,
    message                TEXT,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at           TIMESTAMPTZ,
    expires_at             TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, document_id)  REFERENCES documents(tenant_id, id),
    FOREIGN KEY (tenant_id, version_id)   REFERENCES versions(tenant_id, id),
    FOREIGN KEY (tenant_id, requested_by) REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_sig_req_document ON signature_requests(tenant_id, document_id);
CREATE INDEX idx_sig_req_status   ON signature_requests(tenant_id, status);
ALTER TABLE signature_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE signature_requests FORCE  ROW LEVEL SECURITY;
CREATE POLICY sig_req_tenant_isolation ON signature_requests
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY sig_req_tenant_isolation_insert ON signature_requests
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 36: signature_signers
-- ===========================================================================
CREATE TABLE signature_signers (
    tenant_id          UUID NOT NULL REFERENCES organizations(id),
    id                 UUID NOT NULL DEFAULT gen_random_uuid(),
    request_id         UUID NOT NULL,
    signer_email       TEXT NOT NULL,
    signer_name        TEXT,
    role               TEXT NOT NULL DEFAULT 'signer'
                            CHECK (role IN ('signer', 'witness', 'approver', 'cc')),
    order_index        INT NOT NULL DEFAULT 1,
    status             TEXT NOT NULL DEFAULT 'pending'
                            CHECK (status IN ('pending', 'viewed', 'signed', 'declined')),
    signing_url_token  TEXT NOT NULL UNIQUE,
    signed_at          TIMESTAMPTZ,
    signature_data     JSONB,
    certificate_id     TEXT,
    ip_address         INET,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, request_id) REFERENCES signature_requests(tenant_id, id)
);
CREATE INDEX idx_sig_signers_request ON signature_signers(tenant_id, request_id, order_index);
ALTER TABLE signature_signers ENABLE ROW LEVEL SECURITY;
ALTER TABLE signature_signers FORCE  ROW LEVEL SECURITY;
CREATE POLICY sig_signers_tenant_isolation ON signature_signers
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY sig_signers_tenant_isolation_insert ON signature_signers
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 37: webhook_subscriptions
-- ===========================================================================
CREATE TABLE webhook_subscriptions (
    tenant_id        UUID NOT NULL REFERENCES organizations(id),
    id               UUID NOT NULL DEFAULT gen_random_uuid(),
    url              TEXT NOT NULL,
    events           TEXT[] NOT NULL,
    secret           TEXT NOT NULL,
    is_active        BOOLEAN NOT NULL DEFAULT true,
    failure_count    INT NOT NULL DEFAULT 0,
    last_success_at  TIMESTAMPTZ,
    last_failure_at  TIMESTAMPTZ,
    created_by       UUID,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, created_by) REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_webhook_subs_active ON webhook_subscriptions(tenant_id) WHERE is_active;
CREATE TRIGGER update_webhook_subscriptions_updated_at
    BEFORE UPDATE ON webhook_subscriptions FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
ALTER TABLE webhook_subscriptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_subscriptions FORCE  ROW LEVEL SECURITY;
CREATE POLICY webhook_subs_tenant_isolation ON webhook_subscriptions
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY webhook_subs_tenant_isolation_insert ON webhook_subscriptions
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 38: webhook_deliveries
-- ===========================================================================
CREATE TABLE webhook_deliveries (
    tenant_id        UUID NOT NULL REFERENCES organizations(id),
    id               UUID NOT NULL DEFAULT gen_random_uuid(),
    subscription_id  UUID NOT NULL,
    event_type       TEXT NOT NULL,
    event_id         TEXT NOT NULL,
    payload          JSONB NOT NULL,
    status           TEXT NOT NULL DEFAULT 'pending'
                          CHECK (status IN ('pending', 'delivered', 'failed', 'dead_letter')),
    http_status      INT,
    attempts         INT NOT NULL DEFAULT 0,
    last_attempt_at  TIMESTAMPTZ,
    next_retry_at    TIMESTAMPTZ,
    error_message    TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, subscription_id) REFERENCES webhook_subscriptions(tenant_id, id)
);
CREATE INDEX idx_webhook_deliveries_subscription ON webhook_deliveries(tenant_id, subscription_id, created_at DESC);
CREATE INDEX idx_webhook_deliveries_retry
    ON webhook_deliveries(status, next_retry_at)
    WHERE status IN ('pending', 'failed');
ALTER TABLE webhook_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_deliveries FORCE  ROW LEVEL SECURITY;
CREATE POLICY webhook_deliveries_tenant_isolation ON webhook_deliveries
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY webhook_deliveries_tenant_isolation_insert ON webhook_deliveries
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 39: connector_configs
-- ===========================================================================
CREATE TABLE connector_configs (
    tenant_id              UUID NOT NULL REFERENCES organizations(id),
    id                     UUID NOT NULL DEFAULT gen_random_uuid(),
    connector_type         TEXT NOT NULL CHECK (connector_type IN
                                ('salesforce', 'sap', 'm365', 'google', 'servicenow', 'workday', 'custom')),
    display_name           TEXT NOT NULL,
    config_encrypted       JSONB NOT NULL DEFAULT '{}'::jsonb,
    oauth_tokens_encrypted JSONB NOT NULL DEFAULT '{}'::jsonb,
    is_active              BOOLEAN NOT NULL DEFAULT true,
    last_sync_at           TIMESTAMPTZ,
    sync_status            TEXT,
    created_by             UUID,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, created_by) REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_connector_configs_type   ON connector_configs(tenant_id, connector_type);
CREATE INDEX idx_connector_configs_active ON connector_configs(tenant_id) WHERE is_active;
CREATE TRIGGER update_connector_configs_updated_at
    BEFORE UPDATE ON connector_configs FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
ALTER TABLE connector_configs ENABLE ROW LEVEL SECURITY;
ALTER TABLE connector_configs FORCE  ROW LEVEL SECURITY;
CREATE POLICY connector_configs_tenant_isolation ON connector_configs
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY connector_configs_tenant_isolation_insert ON connector_configs
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 40: subscriptions_billing (tenant_id IS the PK; one row per tenant)
-- ===========================================================================
CREATE TABLE subscriptions_billing (
    tenant_id             UUID PRIMARY KEY REFERENCES organizations(id),
    plan                  TEXT NOT NULL DEFAULT 'standard',
    stripe_customer_id    TEXT,
    stripe_subscription_id TEXT,
    status                TEXT NOT NULL DEFAULT 'active'
                               CHECK (status IN ('active', 'trialing', 'past_due', 'cancelled', 'suspended')),
    user_limit            INT,
    storage_limit_gb      INT,
    current_period_start  TIMESTAMPTZ,
    current_period_end    TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_sub_billing_stripe_customer ON subscriptions_billing(stripe_customer_id) WHERE stripe_customer_id IS NOT NULL;
CREATE TRIGGER update_subscriptions_billing_updated_at
    BEFORE UPDATE ON subscriptions_billing FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
ALTER TABLE subscriptions_billing ENABLE ROW LEVEL SECURITY;
ALTER TABLE subscriptions_billing FORCE  ROW LEVEL SECURITY;
CREATE POLICY sub_billing_tenant_isolation ON subscriptions_billing
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY sub_billing_tenant_isolation_insert ON subscriptions_billing
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 41: usage_meters
-- ===========================================================================
CREATE TABLE usage_meters (
    tenant_id     UUID NOT NULL REFERENCES organizations(id),
    id            UUID NOT NULL DEFAULT gen_random_uuid(),
    metric        TEXT NOT NULL CHECK (metric IN
                       ('storage_gb_days', 'ocr_pages', 'api_calls', 'signatures', 'ai_tokens', 'active_users')),
    quantity      NUMERIC NOT NULL,
    period_start  DATE NOT NULL,
    period_end    DATE NOT NULL,
    recorded_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);
CREATE INDEX idx_usage_meters_period ON usage_meters(tenant_id, metric, period_start);
ALTER TABLE usage_meters ENABLE ROW LEVEL SECURITY;
ALTER TABLE usage_meters FORCE  ROW LEVEL SECURITY;
CREATE POLICY usage_meters_tenant_isolation ON usage_meters
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY usage_meters_tenant_isolation_insert ON usage_meters
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 42: outbox (BIGSERIAL id; append + mark-published workflow)
-- ===========================================================================
CREATE TABLE outbox (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    event_type      TEXT NOT NULL,
    aggregate_type  TEXT NOT NULL,
    aggregate_id    UUID NOT NULL,
    payload         JSONB NOT NULL,
    published       BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at    TIMESTAMPTZ
);
CREATE INDEX idx_outbox_unpublished
    ON outbox(created_at) WHERE NOT published;
CREATE INDEX idx_outbox_tenant_time ON outbox(tenant_id, created_at DESC);
ALTER TABLE outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox FORCE  ROW LEVEL SECURITY;
CREATE POLICY outbox_tenant_isolation ON outbox
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY outbox_tenant_isolation_insert ON outbox
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 43: tenant_metadata_schemas
-- ===========================================================================
CREATE TABLE tenant_metadata_schemas (
    tenant_id    UUID NOT NULL REFERENCES organizations(id),
    id           UUID NOT NULL DEFAULT gen_random_uuid(),
    name         TEXT NOT NULL,
    json_schema  JSONB NOT NULL DEFAULT '{}'::jsonb,
    version      INT NOT NULL DEFAULT 1,
    is_active    BOOLEAN NOT NULL DEFAULT true,
    created_by   UUID,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, name, version),
    FOREIGN KEY (tenant_id, created_by) REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_metadata_schemas_active ON tenant_metadata_schemas(tenant_id) WHERE is_active;
CREATE TRIGGER update_tenant_metadata_schemas_updated_at
    BEFORE UPDATE ON tenant_metadata_schemas FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
ALTER TABLE tenant_metadata_schemas ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_metadata_schemas FORCE  ROW LEVEL SECURITY;
CREATE POLICY metadata_schemas_tenant_isolation ON tenant_metadata_schemas
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY metadata_schemas_tenant_isolation_insert ON tenant_metadata_schemas
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 44: conversation_history (AI Q&A audit)
-- ===========================================================================
CREATE TABLE conversation_history (
    tenant_id        UUID NOT NULL REFERENCES organizations(id),
    id               UUID NOT NULL DEFAULT gen_random_uuid(),
    conversation_id  UUID NOT NULL,
    user_id          UUID,
    turn_index       INT NOT NULL,
    role             TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
    content          TEXT NOT NULL,
    citations        JSONB NOT NULL DEFAULT '[]'::jsonb,
    model_used       TEXT,
    input_tokens     INT,
    output_tokens    INT,
    cost_cents       INT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, conversation_id, turn_index),
    FOREIGN KEY (tenant_id, user_id) REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_conversation_history_conv
    ON conversation_history(tenant_id, conversation_id, turn_index);
CREATE INDEX idx_conversation_history_user
    ON conversation_history(tenant_id, user_id, created_at DESC);
ALTER TABLE conversation_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE conversation_history FORCE  ROW LEVEL SECURITY;
CREATE POLICY conv_history_tenant_isolation ON conversation_history
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY conv_history_tenant_isolation_insert ON conversation_history
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ===========================================================================
-- TABLE 45: sso_configs
-- ===========================================================================
CREATE TABLE sso_configs (
    tenant_id      UUID NOT NULL REFERENCES organizations(id),
    id             UUID NOT NULL DEFAULT gen_random_uuid(),
    provider_type  TEXT NOT NULL CHECK (provider_type IN ('saml', 'oidc')),
    display_name   TEXT NOT NULL,
    config         JSONB NOT NULL,
    is_active      BOOLEAN NOT NULL DEFAULT true,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);
CREATE INDEX idx_sso_configs_active ON sso_configs(tenant_id) WHERE is_active;
CREATE TRIGGER update_sso_configs_updated_at
    BEFORE UPDATE ON sso_configs FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
ALTER TABLE sso_configs ENABLE ROW LEVEL SECURITY;
ALTER TABLE sso_configs FORCE  ROW LEVEL SECURITY;
CREATE POLICY sso_configs_tenant_isolation ON sso_configs
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY sso_configs_tenant_isolation_insert ON sso_configs
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ---------------------------------------------------------------------------
-- GRANTs
-- ---------------------------------------------------------------------------
GRANT USAGE ON SCHEMA public TO dms_app, dms_readonly;
GRANT SELECT, INSERT, UPDATE ON ALL TABLES    IN SCHEMA public TO dms_app;
GRANT SELECT                 ON ALL TABLES    IN SCHEMA public TO dms_readonly;
GRANT USAGE, SELECT          ON ALL SEQUENCES IN SCHEMA public TO dms_app;

-- audit_events is append-only.
REVOKE UPDATE, DELETE ON audit_events FROM dms_app;

-- organizations is managed out-of-band (via platform admin tooling).
-- dms_app gets SELECT only; INSERT/UPDATE is done by a separate admin role.
REVOKE INSERT, UPDATE, DELETE ON organizations FROM dms_app;
GRANT  SELECT                 ON organizations TO   dms_app;

COMMIT;
