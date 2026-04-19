# Per-tenant KEK — deferred

## Status
Deferred. Single KEK shared across all tenants.
Source reference: `services/storage/cmd/server/main.go:137`.
Audit reference: `docs/audit/04-antipatterns.md` finding **k** (MEDIUM severity).

## Current behavior

All tenants' content blobs are encrypted with per-blob DEKs wrapped by a
single KEK identified as `"vaultdms-storage-default"`. The KEK is loaded
at startup from `VAULTDMS_LOCAL_KEK` (env var) for the `local` KMS
provider, or resolved against Vault / AWS KMS in the respective provider
mode.

Impact:
- **Blast radius on compromise.** If the single KEK is leaked, every
  tenant's content is exposed. Per-tenant KEKs would bound the blast
  radius to the compromised tenant.
- **Key rotation is global.** Rotating the KEK re-wraps every DEK across
  the entire fleet in one operation — no way to rotate one tenant
  independently (e.g. after a customer incident or contractual
  requirement).
- **Compliance.** Enterprise customers in regulated industries often
  require demonstrable key isolation per tenant (HIPAA, FedRAMP High).

## Target design

1. **Schema addition.** New table `tenant_kms_config`:
   ```sql
   CREATE TABLE tenant_kms_config (
     tenant_id   UUID PRIMARY KEY REFERENCES organizations(id),
     provider    TEXT NOT NULL,          -- local | vault | aws_kms
     kek_id      TEXT NOT NULL,          -- provider-specific identifier
     kek_version INT  NOT NULL DEFAULT 1,
     created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
     rotated_at  TIMESTAMPTZ
   );
   ```

2. **Startup behavior.** Remove the hardcoded `TenantKEKID` in
   `storage/cmd/server/main.go`. Instead, the service resolves the KEK
   per request via `ResolveTenantKEK(tenantID) → kekID`. Cache the
   resolution in Redis (`tenant_kek:{tenantID}`, 10 min TTL).

3. **Provisioning.** The billing/provisioner service, on tenant creation,
   generates a new KEK (via the tenant's chosen KMS provider) and writes
   the row to `tenant_kms_config`. This is an additive step in
   `services/billing/internal/provisioner/provisioner.go`.

4. **Per-blob metadata.** `content_blobs.kek_id` already carries the KEK
   identifier used at encryption time (no schema change needed). The
   `kek_version` field can be appended to support rotation scenarios.

5. **Decryption path.** Unchanged — `blob.kek_id` tells the decrypt path
   which KEK to use. Existing blobs with `kek_id = "vaultdms-storage-default"`
   continue to decrypt against the legacy shared KEK until migrated.

## Migration plan

Two options depending on tolerance for downtime:

### Option A — Big-bang re-wrap (requires downtime)

1. Set the service to read-only (reject uploads, allow reads).
2. For each tenant: generate a new per-tenant KEK, add to `tenant_kms_config`.
3. Walk every `content_blobs` row for that tenant:
   - Decrypt the existing `encrypted_dek` using the legacy shared KEK
   - Re-wrap the DEK using the new per-tenant KEK
   - Update `content_blobs` row: `encrypted_dek`, `kek_id`, `kek_version`
4. Once all tenants migrated, remove the legacy KEK from the KMS.
5. Restore read-write mode.

Estimated downtime at 10M blobs and 1000 re-wraps/sec = ~3 hours.

### Option B — Live lazy re-wrap (no downtime)

1. Generate new per-tenant KEKs as in A step 2.
2. Keep the legacy KEK available in the KMS.
3. On every blob **read**, check `kek_id`:
   - If `= "vaultdms-storage-default"` → decrypt with legacy, re-wrap with
     per-tenant KEK, UPDATE the row in a transaction, then return the
     plaintext to the caller.
   - If `= tenant-specific KEK` → decrypt normally.
4. Run a background walker that proactively re-wraps cold blobs
   (prioritize by `last_accessed_at`) at a configurable rate (e.g.
   1000 blobs/sec during off-peak).
5. After ~30 days (or when a sampling check shows <1% residual rows),
   audit `content_blobs` for any remaining legacy `kek_id`. Hot-migrate
   those, then retire the legacy KEK.

Option B is preferred — no downtime, natural priority ordering (hot
blobs migrate first on access).

## Required code changes (when prioritized)

- `services/storage/cmd/server/main.go` — remove hardcoded `TenantKEKID`
- `services/storage/internal/service/service.go` — add `resolveKEK(tenantID)`,
  inject into the encrypt path
- `services/storage/internal/service/encryption.go` — accept `kekID` parameter
- New migration: `tenant_kms_config` table + index on `tenant_id`
- `services/billing/internal/provisioner/provisioner.go` — call
  `StorageService.ProvisionKEK(tenantID)` during tenant creation
- Background re-wrapper goroutine in storage service (Option B)

## Operational considerations

- **KEK rotation.** Each tenant's KEK gets its own rotation schedule.
  Target: 1-year rotation for customer-managed; 2-year for platform-managed.
- **Caching.** Cache `tenant_kek:{tenantID}` in Redis to avoid a DB hit
  on every upload. Invalidate on rotation.
- **Observability.** New metrics: `kek_resolve_duration`,
  `kek_cache_hit_ratio`, `blobs_pending_rewrap_count` (Option B).

## Ownership

[assign before starting work]

## Cross-refs

- Audit: `docs/audit/04-antipatterns.md` finding k
- Code TODO: `services/storage/cmd/server/main.go:137`
