-- Align tenant_metadata_schemas with how the Go repository actually uses it.
--
-- services/document/internal/repository/misc_repo.go runs:
--   INSERT INTO tenant_metadata_schemas (tenant_id, json_schema, updated_by, updated_at)
--   VALUES (...)
--   ON CONFLICT (tenant_id) DO UPDATE SET ...
--
-- Two prerequisites the table was missing:
--
--   1. ON CONFLICT (tenant_id) needs a UNIQUE constraint on tenant_id alone.
--      The initial schema only had UNIQUE (tenant_id, name, version) which
--      modeled a multi-named-version-per-tenant table — never what the
--      handler actually uses. Singleton-per-tenant is the real shape.
--
--   2. The INSERT does not supply `name`, but the column is NOT NULL with
--      no default — first save 500'd with a NULL constraint violation
--      right after the ON CONFLICT mismatch was patched.
--
-- The older 3-column UNIQUE constraint is left in place; it's now
-- subsumed by the new tenant-only unique (one row per tenant means at
-- most one (tenant, name, version) combo) so it costs nothing and
-- preserves the original schema intent if someone ever resurrects
-- multi-version metadata schemas via a future migration.
--
-- IF NOT EXISTS / pg_constraint guards so a hot-fix already applied to
-- a running tenant doesn't break this migration when migrate-up runs.

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'tenant_metadata_schemas_tenant_id_singleton'
  ) THEN
    ALTER TABLE tenant_metadata_schemas
      ADD CONSTRAINT tenant_metadata_schemas_tenant_id_singleton UNIQUE (tenant_id);
  END IF;
END $$;

ALTER TABLE tenant_metadata_schemas
  ALTER COLUMN name SET DEFAULT 'default';
