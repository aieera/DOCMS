-- Stable external key (Workstream "Stable external key + upsert-by-external-key").
--
-- Give documents a first-class, tenant-unique business key (e.g.
-- "INV-2024-00188") so the ERP can upsert + append versions by the key it
-- already owns, instead of SeDoc's internal UUID. This supersedes the
-- create-only dedupe of bulk_external_id_map (ADR 0075), which is kept as a
-- compatibility shim and still written by the bulk importer.
--
-- The partial unique index is the load-bearing invariant: at most one document
-- per (tenant, external_id). Combined with the upsert handler's
-- INSERT ... ON CONFLICT, it makes concurrent upserts of the same key
-- converge on exactly one document (no duplicate) at the DB layer.
ALTER TABLE documents ADD COLUMN IF NOT EXISTS external_id TEXT;

-- Backfill from the existing bulk import map so documents created via the
-- ADR-0075 bulk importer become discoverable / upsertable by their business
-- key. The map's UNIQUE (tenant_id, resource_type, external_id) guarantees a
-- single internal_id per key, so no row can collide on the new index. Runs as
-- the migration role (superuser), which bypasses the documents FORCE RLS
-- policy, so the cross-tenant join is intentional and safe here.
UPDATE documents d
   SET external_id = m.external_id
  FROM bulk_external_id_map m
 WHERE m.tenant_id     = d.tenant_id
   AND m.resource_type = 'document'
   AND m.internal_id   = d.id
   AND d.external_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_documents_external_id
    ON documents (tenant_id, external_id)
    WHERE external_id IS NOT NULL;
