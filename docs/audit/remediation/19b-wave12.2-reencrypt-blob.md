# Remediation 19b — Wave 12.2: Storage cross-region re-encrypt + move

**Date:** 2026-04-18
**Wave:** 12.2 · completes Wave 8.4 residency migration.

## Recon

Wave 8.4 shipped the residency dashboard + `MigrateDocuments`
Temporal workflow. The workflow flipped `documents.region_pin` +
emitted `dms.residency.migrated.v1` per doc — but the underlying
ciphertext stayed in the source-region bucket under the
source-region KEK. Operators got correct metadata and incorrect
cryptography.

## What shipped

### Repo: UpdateMigration

[services/storage/internal/repository/content_blobs.go](../../../services/storage/internal/repository/content_blobs.go)
— new method on `ContentBlobRepo`:

```go
UpdateMigration(tx, b *ContentBlob) error
```

Rotates `storage_region`, `storage_bucket`, `storage_key`,
`encrypted_dek`, `dek_nonce`, `kek_id` in one UPDATE. Sha256 is
preserved (same plaintext). Reference count untouched — re-encrypt
preserves blob sharing.

### Service: ReencryptBlob

[services/storage/internal/service/reencrypt.go](../../../services/storage/internal/service/reencrypt.go)
— `ReencryptBlob(ctx, {TenantID, BlobID, TargetRegion})`:

1. Load `content_blobs` row (tenant-scoped, RLS).
2. Idempotence: already in target region under target-scoped KEK
   alias → no-op result.
3. GET ciphertext from source bucket.
4. Unwrap DEK under source KEK alias.
5. Generate fresh DEK under target region's alias
   (`aliasForTenantInRegion` — ADR 0026 per-region KEK).
6. AES-GCM re-encrypt.
7. PUT to target bucket.
8. UPDATE `content_blobs` row atomically.
9. DELETE source object **only after DB commit** — if UPDATE
   errors, the orphan target is logged for the Wave 12
   reconciliation job and the source is still intact for retry.

Security invariants:

- DEK plaintext is zeroed on both happy-path and error-path
  (defer + explicit wipe before return).
- Source bucket deletion skipped when source == target
  (single-region MinIO dev where buckets are the same
  object store).
- Caller tenant flows through `WithTenantTx` on load and
  update — cross-tenant migration attempts hit RLS and return
  0 rows affected → `ErrNotFound`.

### Tests

[services/storage/internal/service/reencrypt_test.go](../../../services/storage/internal/service/reencrypt_test.go)
— 4 unit tests covering the pure-Go contracts:

1. `AliasForTenantInRegion_AppendsRegion` — regional alias is
   `/region` suffix of the base alias.
2. `AliasForTenantInRegion_EmptyRegionFallsBack` — trim +
   backwards-compat with pre-11.7 data.
3. `BucketName_IsRegionScoped` — target bucket name derives from
   region + tier.
4. `IdempotenceShape_DetectsSameRegionAndAlias` — pins the
   comparison triplet the service uses to short-circuit a no-op
   migration.

Full end-to-end (real pool + S3 + KMS) lives in the Wave 13.1
integration suite.

```
$ go test ./services/storage/...
ok  github.com/vaultdms/vaultdms/services/storage/internal/service  0.335s
```

## DoD

| Requirement | Status |
|---|---|
| Move ciphertext to target region | ✅ `S3.PutObject` to `dms-<region>-<tier>` |
| Re-encrypt under region-local KEK | ✅ ADR 0026 alias via `aliasForTenantInRegion` |
| Idempotent on retry | ✅ same-region+alias → no-op result |
| DB atomicity | ✅ single `WithTenantTx` UPDATE |
| Orphan cleanup safety | ✅ source delete only after commit |
| Tenant isolation | ✅ RLS + WithTenantTx |

## Deferred (logged in out-of-scope.md)

- **Workflow integration** — `MoveDocumentRegion` activity in the
  residency workflow still only flips `region_pin`. Wiring it to
  call `ReencryptBlob` for each content_blob associated with the
  document requires either an RPC from the workflow service to
  storage, or moving the re-encrypt into a new workflow activity.
  Wave 12.2b.
- **Multi-endpoint S3 client** — `S3Client` today points at one
  endpoint (single MinIO or single-region S3). Cross-region in
  AWS works because S3 is globally addressable, but the API needs
  a `ClientForRegion(region) *S3Client` accessor for pathological
  cases (e.g. a GovCloud region with a separate endpoint). Wave 12
  follow-up.
- **Reaper reconciliation** — when UPDATE fails post-PUT, the
  target-bucket ciphertext is orphaned. A periodic reconcile
  query (compare S3 listing vs `content_blobs.storage_key`) finds
  + deletes orphans. Wave 12 follow-up.
- **Admin CLI** — `dms-admin migrate-blob --blob <id> --to
  <region>` for manual operator-driven migrations. Wave 12
  follow-up.

## Wave 12 scorecard

| Item | Status |
|---|---|
| 12.1 SMTP transactional email | ✅ |
| **12.2 Storage cross-region re-encrypt** | ✅ this doc |
| 12.3 Background re-wrap for regional KEKs | pending |
| 12.4 Qdrant / OpenSearch subject erase | pending |
| 12.5 Redaction fan-out to intelligence | pending |
| 12.6 Connector OAuth token purge | pending |
| 12.7 Control-plane admin endpoints | pending |
| 12.8 Vault / AWS KMS production adapters | pending |
| 12.9 DSS Java sidecar | pending |

## Next prompt

**12.3 — Background re-wrap job for regional KEKs.** Legacy
`vaultdms/tenant/<uuid>` DEKs (pre-ADR 0026) need to migrate to
regional aliases. A `dms-admin kms rewrap-regional --tenant <id>`
CLI reads each blob's region_pin, calls `ReencryptBlob` per blob
(which is now idempotent), and reports progress. Closes the
ADR 0026 §Consequences "background re-wrap" deferral.
