-- Per-tenant upload format allowlist. Admin-curated MIME types and
-- file extensions; an empty list = "no allowlist enforced" so the
-- pre-existing executable blocklist (services/storage/internal/scanner/
-- mimecheck.go) remains the only gate, preserving prior behaviour for
-- tenants who haven't opted in.
--
-- Read by the storage service's InitiateUpload (same DB pool) AND by
-- the document-service REST handler that admins call from the
-- /admin/tenant/upload-policy page. Per-user allowlists live on the
-- users.settings JSONB and must always be a SUBSET of this list.

CREATE TABLE IF NOT EXISTS tenant_upload_policies (
    tenant_id          UUID         PRIMARY KEY,
    allowed_mime_types JSONB        NOT NULL DEFAULT '[]'::jsonb,
    allowed_extensions JSONB        NOT NULL DEFAULT '[]'::jsonb,
    updated_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_by         UUID
);

ALTER TABLE tenant_upload_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_upload_policies FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_upload_policies_isolation ON tenant_upload_policies;
CREATE POLICY tenant_upload_policies_isolation ON tenant_upload_policies
    USING (tenant_id::text = current_setting('app.current_tenant', true));
