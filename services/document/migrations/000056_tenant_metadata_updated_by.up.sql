-- Add the missing updated_by column to tenant_metadata_schemas.
--
-- The Go repository at services/document/internal/repository/misc_repo.go
-- writes this column on every UPSERT (PUT /api/v1/tenants/metadata-schema)
-- but the initial schema (000001) created the table without it, and no
-- subsequent migration added it. Every save 500'd with:
--   ERROR: column "updated_by" of relation "tenant_metadata_schemas"
--   does not exist (SQLSTATE 42703)
--
-- Mirrors the shape of created_by (nullable UUID + composite FK to
-- users(tenant_id, id)).
--
-- IF NOT EXISTS / pg_constraint guard so a hot-fix that already added
-- the column manually on a running tenant doesn't break this migration
-- when it eventually runs.

ALTER TABLE tenant_metadata_schemas ADD COLUMN IF NOT EXISTS updated_by UUID;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'tenant_metadata_schemas_tenant_id_updated_by_fkey'
  ) THEN
    ALTER TABLE tenant_metadata_schemas
      ADD CONSTRAINT tenant_metadata_schemas_tenant_id_updated_by_fkey
      FOREIGN KEY (tenant_id, updated_by) REFERENCES users(tenant_id, id);
  END IF;
END $$;
