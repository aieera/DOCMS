DROP INDEX IF EXISTS idx_documents_tenant_region;

ALTER TABLE organizations
    DROP COLUMN IF EXISTS default_region_pin,
    DROP COLUMN IF EXISTS allowed_regions;

DROP TABLE IF EXISTS supported_regions;
