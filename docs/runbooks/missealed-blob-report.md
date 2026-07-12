# Runbook: missealed-blob residency report

## What this finds

Before the seal region-pin fix (ADR 0025 follow-up), `SealVersion` /
`SealCeremony` wrote the sealed blob **without a RegionPin**, so the
storage service defaulted it to `us-east-1`. Any document pinned to a
non-us-east region therefore has one or more **sealed versions whose
bytes live in the wrong region** — a data-residency breach.

The report ([scripts/report-missealed-seal-blobs.sql](../../scripts/report-missealed-seal-blobs.sql))
lists every sealed/signed version whose `content_blobs.storage_region`
differs from the owning document's `region_pin`, plus a per-tenant
summary. It is **read-only** — it scopes the blast radius; the actual
cross-region move is a separate migration ticket (below).

## Running it

Operator / superuser (spans all tenants; RLS would otherwise hide rows):

```bash
psql "$DATABASE_URL" -f scripts/report-missealed-seal-blobs.sql
```

Single tenant: append `AND d.tenant_id = '<uuid>'` to each query's WHERE.

The first result set is the offending `(document, version, blob)` rows
with both regions; the second is a `(tenant, pinned_region,
blob_region) → count` summary to size the migration.

## Interpreting

- **0 rows** → no missealed blobs; nothing to migrate. (Expected once the
  fix has been deployed for a full seal cycle and no legacy backlog
  exists.)
- **rows present** → each is a sealed artifact stored outside its pinned
  region. The document is still readable, but the *sealed bytes at rest*
  violate residency. Feed the summary into the migration ticket.

## Migration (follow-up ticket, out of scope for the fix PR)

For each missealed blob:
1. Re-upload the blob's bytes into the pinned region via the storage
   service (`InitiateUpload` with the correct `region_pin` → PUT →
   `CompleteUpload`), producing a new `content_blob` in-region.
2. Repoint the sealed `document_versions` row at the new blob.
3. Crypto-shred / delete the mis-regioned blob (drop its bytes from the
   wrong region) once the repoint is verified.

This is the compliance-migration-tool shape (cross-region moves already
exist for `region_pin` changes); the seal case is a new input to it. The
report is the authoritative work-list.

## Prevention

The fix makes this class impossible going forward: `PutAndCreateVersion`
and the gRPC `PutSignedBlob` primitive both **fail closed** on an unset
`RegionPin`, and the seal paths resolve the document's pin
(`ResolveRegionOrFail`) before writing — an unresolvable pin rejects the
seal rather than defaulting a region.
