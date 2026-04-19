# Runbook — Per-tenant KEK management (Wave 6 Prompt 6.1)

**Last rehearsed:** 2026-04-17 (dev box: create, list, rotate all verified live).
**On-call:** security + platform.

## What this covers

Every encrypted object in `content_blobs` carries:
- `encrypted_dek` — the per-object AES-256 data key, wrapped.
- `dek_nonce` — the AES-GCM nonce used during object encryption.
- `kek_id` — the alias that identifies **which per-tenant KEK** wraps
  the DEK. Format: `vaultdms/tenant/<uuid>` for v1 or
  `vaultdms/tenant/<uuid>@v<N>` for later versions.

Decrypt-on-download path: read `kek_id` off the blob row → KeyManager
resolves the alias → unwrap DEK → AES-GCM decrypt object bytes.

## Key commands

```bash
dms-admin kms list                              # all tenants
dms-admin kms list --tenant <uuid>              # one tenant's history
dms-admin kms create --tenant <uuid>            # mint v1 for a new tenant
dms-admin kms rotate --tenant <uuid>            # retire live, mint v+1
```

## Creating a tenant KEK

Every organization in `organizations` **must** have at least one row in
`tenant_keks`. Migration 000004 seeds v1 for all existing tenants. For
tenants created after 6.1 ships, the tenant-provisioning workflow
(Wave 12.3) is expected to call `dms-admin kms create` — until that
lands, run manually:

```bash
dms-admin kms create --tenant <new-tenant-uuid>
```

Missing `tenant_keks` row = encrypt fails with a non-empty-alias guard
(`LocalKeyManager: empty kekID`).

## Rotation flow

Rotation is **non-destructive**:

1. `kms rotate --tenant X` retires the current live row (`retired_at =
   now()`) and inserts a new row at `version + 1` with the versioned
   alias (`@v<N>`).
2. The storage service reads the live alias at the top of each
   `CompleteUpload` via `aliasForTenant()`. The next write uses the new
   alias.
3. Historical blobs (those written under the previous alias) **still
   decrypt** because `content_blobs.kek_id` preserves the alias that
   was used at write time.

### When to rotate

- Suspected KEK compromise → rotate + re-wrap immediately
  (re-wrap CLI deferred to Wave 6 follow-up; until then, manually
  re-upload affected objects or flag for scheduled rotation).
- Scheduled: every 90 days for HIPAA / SOC 2 compliance.
- Master-secret rotation: `VAULTDMS_LOCAL_KEK` rotated (dev/on-prem)
  also invalidates every derived tenant KEK. Never rotate the master
  without first running `kms rewrap --all` (Wave 6 follow-up).

## ADR

Full derivation strategy + alternatives considered: [0022-per-tenant-kek-derivation](../adr/0022-per-tenant-kek-derivation.md).

## Common failure modes

### Decrypt fails with `cipher: message authentication failed`

Three possibilities, in order of likelihood:
1. `content_blobs.kek_id` value drifted from what was used at write
   time (bug; file a P0).
2. `VAULTDMS_LOCAL_KEK` master secret on the reading node differs from
   the writing node. Check Kubernetes Secret `vaultdms-master-kek`
   parity across pods.
3. An AWS KMS / Vault outage — the KeyManager implementation can
   surface a network error as "decrypt failed". Check
   `kms_decrypt_errors_total` metric (added in Wave 6 follow-up).

### Rotation says `no live KEK for tenant`

The tenant exists in `organizations` but has no row in `tenant_keks`.
Happens for tenants created before Wave 6 in a deployment that skipped
migration 000004. Fix: run `dms-admin kms create --tenant <uuid>`.

## Rollback

The per-tenant KEK path is load-bearing. Rolling back means:

1. Revert `services/storage/internal/service/service.go` to use the
   literal `"vaultdms-storage-default"` alias.
2. Revert `pkg/crypto/kms.go` `LocalKeyManager` to the single-KEK
   form.
3. New encrypts fall back to the shared key. Historical
   per-tenant-encrypted objects **no longer decrypt** (wrong alias in
   `content_blobs.kek_id`).
4. This is destructive. Don't do it. If there's an incident, rotate
   instead.

## Known deferred

- **Online re-wrap** (`dms-admin kms rewrap --tenant X`) — would
  stream every blob for the tenant, decrypt with the old alias,
  re-wrap the DEK with the new alias, update the row. Scope deferred
  to Wave 6 follow-up; critical-path blocked objects can be fixed by
  re-upload today.
- **Vault / AWS KMS providers** — `pkg/crypto.VaultKeyManager` +
  `AWSKMSKeyManager` are stubs. Local HKDF carries dev + on-prem.
- **Master-secret rotation** (the secret itself, not per-tenant
  rotation) — coupled to `dms-admin secrets rotate --target
  master-kek`, a Wave 12.2 deliverable.
