-- Missealed-blob report (residency audit) — ADR 0025 / seal region-pin fix.
--
-- Before the fix, SealVersion/SealCeremony wrote the sealed blob with no
-- RegionPin, so the storage service defaulted it to us-east-1. For any
-- document pinned to a non-us-east region, the sealed version's bytes
-- therefore landed in the WRONG region — a data-residency breach.
--
-- This report finds already-sealed versions whose blob region does not
-- match the owning document's region_pin. It is READ-ONLY. The mass
-- cross-region move (re-upload each blob into the pinned region + repoint
-- the version, or crypto-shred + re-seal) is a separate migration ticket;
-- this report scopes the blast radius.
--
-- Run as an operator (superuser / bypass-RLS) so it spans all tenants:
--   psql "$DATABASE_URL" -f scripts/report-missealed-seal-blobs.sql
-- For a single tenant, add:  AND d.tenant_id = '<tenant-uuid>'

-- 1. The offending rows.
SELECT
    d.tenant_id,
    d.id                         AS document_id,
    d.region_pin                 AS pinned_region,
    v.id                         AS version_id,
    v.version_number,
    v.change_summary,
    cb.id                        AS content_blob_id,
    cb.storage_region            AS blob_region,
    cb.storage_bucket,
    cb.storage_key,
    cb.created_at                AS blob_created_at
FROM document_versions v
JOIN documents d
      ON d.tenant_id = v.tenant_id AND d.id = v.document_id
JOIN content_blobs cb
      ON cb.tenant_id = v.tenant_id AND cb.id = v.content_blob_id
WHERE
    -- Sealed / signed versions are produced by the seal paths with these
    -- change_summary prefixes (seal.go). Vendor-envelope signed versions
    -- carry "Signed via <provider> envelope" (esign.go).
    (v.change_summary LIKE 'Server seal%'
     OR v.change_summary LIKE 'Signing ceremony%'
     OR v.change_summary LIKE 'Signed via %envelope%')
    AND cb.storage_region IS DISTINCT FROM d.region_pin
ORDER BY d.tenant_id, cb.created_at;

-- 2. Summary by tenant + region mismatch, for the migration ticket.
SELECT
    d.tenant_id,
    d.region_pin        AS pinned_region,
    cb.storage_region   AS blob_region,
    count(*)            AS missealed_versions,
    min(cb.created_at)  AS earliest,
    max(cb.created_at)  AS latest
FROM document_versions v
JOIN documents d
      ON d.tenant_id = v.tenant_id AND d.id = v.document_id
JOIN content_blobs cb
      ON cb.tenant_id = v.tenant_id AND cb.id = v.content_blob_id
WHERE
    (v.change_summary LIKE 'Server seal%'
     OR v.change_summary LIKE 'Signing ceremony%'
     OR v.change_summary LIKE 'Signed via %envelope%')
    AND cb.storage_region IS DISTINCT FROM d.region_pin
GROUP BY d.tenant_id, d.region_pin, cb.storage_region
ORDER BY missealed_versions DESC;
