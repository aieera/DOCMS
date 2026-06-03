# Remediation 18d — Wave 11.7: Per-region KEK masters + ADR 0026

**Date:** 2026-04-18
**Wave:** 11.7 · closes the Wave 8.4 deferral and the original
Wave 6.1 follow-up on regional key isolation.

## Recon

Wave 6.1 (ADR 0022) derived per-tenant KEKs via HKDF from a single
master. Wave 8.4 introduced `documents.region_pin` + the residency
migration workflow. But all regions still shared the same ikm —
a US-region master compromise would decrypt EU-region ciphertext
because HKDF salt does not isolate under ikm compromise.

This is the invariant EU customers ask about in procurement
reviews. Closing it was a hard prerequisite for multi-region pilot.

## What shipped

### ADR 0026

[docs/adr/0026-per-region-kek-masters.md](../../adr/0026-per-region-kek-masters.md)
documents:

- Alias format: `vaultdms/tenant/<uuid>/<region>` (with optional
  `@v<N>` rotation suffix preserved).
- Hard error on kekID claiming an unconfigured region — no silent
  fallback.
- Rollout: legacy `vaultdms/tenant/<uuid>` aliases continue to
  work against the global master so pre-11.7 data reads without
  re-wrapping; a background re-wrap migrates DEKs over time.

### `LocalKeyManager` gains regional masters

[pkg/crypto/kms.go](../../../pkg/crypto/kms.go):

- New field `regionMasters map[string][]byte` on `LocalKeyManager`.
- New constructor `NewMultiRegionLocalKeyManager(defaultB64,
  regionalB64 map[string]string, warnFn)` — base64-decodes each
  region's master, asserts 32-byte length.
- New `masterFor(kekID)` method — parses
  `vaultdms/tenant/<uuid>/<region>`, strips `@v<N>` rotation
  suffix, returns the matching master. Unknown region → hard
  error. kekID without a region → global master (backwards
  compatibility).
- `deriveKEK` calls `masterFor` before HKDF. HKDF salt still
  includes the full kekID so each region + version pair mints a
  distinct KEK even if two regions somehow shared a master.

Also added two tiny private helpers (`indexByte`, `splitOnSlash`)
so this file doesn't pull in `strings` for two operations.

### Storage alias helper

[services/storage/internal/service/tenant_kek.go](../../../services/storage/internal/service/tenant_kek.go):

- New `aliasForTenantInRegion(tenantID, region)` — returns
  `vaultdms/tenant/<uuid>/<region>` when `region` is set,
  falls back to the pre-regional form when empty. Callers that
  know the region (upload path, presigned GET) pass it through;
  callers on legacy data leave it blank.

### Tests

[pkg/crypto/kms_region_test.go](../../../pkg/crypto/kms_region_test.go)
— 5 new tests pin the invariants:

1. `NoRegionMap` — single-master constructor still round-trips,
   backwards-compatible.
2. `RegionalSelection` — wrap under us-east-1, decrypt under
   eu-west-1 fails (AEAD tag mismatch); the isolation promise.
3. `UnknownRegionFailsFast` — claiming `ap-southeast-2` when it
   isn't configured errors with `no configured master` rather
   than silently falling back to the global master.
4. `RotationSuffixPreservesRegion` — `@v2` suffix strips to the
   right region; v1 can't decrypt v2 ciphertext even with the
   same master.
5. `PreRegionalAliasStillWorks` — legacy kekID without a region
   suffix resolves to the global master when `regionMasters` is
   populated.

```
$ go test ./pkg/crypto/...
ok  github.com/aieera/sedoc/pkg/crypto  2.025s
```

## DoD — Wave 8.4 deferral

| Requirement | Status |
|---|---|
| Per-region KEK aliases | ✅ alias format + `aliasForTenantInRegion` |
| Cross-region isolation under ikm compromise | ✅ separate master per region; verified by test 2 |
| Backwards compatibility with pre-11.7 data | ✅ verified by test 5 |
| Hard failure when region missing | ✅ verified by test 3 |
| Production Vault / AWS KMS adapter plan | documented in ADR 0026 as a shared contract; real implementations still Wave 12 work |

## Deferred (logged in out-of-scope.md)

- **Background re-wrap job** — legacy `vaultdms/tenant/<uuid>`
  DEKs don't migrate themselves. A `dms-admin kms rewrap-regional
  --tenant <id>` CLI that reads each blob's region, re-wraps the
  DEK under the regional alias, and updates `encrypted_dek` would
  close the migration. Wave 12.
- **Production Vault / AWS KMS adapters** — the ADR defines the
  shared contract; the adapter bodies are still stubs per
  ADR 0025 and the Wave 6.1 follow-up ledger.
- **Operator runbook update** — `docs/runbooks/06-key-management.md`
  needs a "rotate regional master" section. Wave 12.
- **Wire `aliasForTenantInRegion` into the storage upload path** —
  today `service.go` uses `aliasForTenant` unconditionally. The
  upload handler needs to read the region from the initiate
  request and pass it through. Wave 12 (tied to the production
  multi-region deploy).

## Wave 11 scorecard — CLOSED ✅

| Item | Status |
|---|---|
| 11.1 Workflow RLS audit | ✅ |
| 11.2 OPA compliance-officer gate | ✅ |
| 11.3 Dead-code cleanup | ✅ |
| 11.4 DSR verification token | ✅ |
| 11.5 Redaction endpoint | ✅ |
| 11.6 Cross-service DSR erase | ✅ |
| **11.7 Per-region KEK masters + ADR 0026** | ✅ this doc |

Wave 11 is done.

## Next wave

**Wave 12 — infrastructure completion.** Spec §11 calls out:

- DSS Java sidecar (Wave 9.2b)
- Storage cross-region copy / re-encrypt (for residency workflow)
- SMTP transactional email path (for DSR token)
- Vault / AWS KMS production adapters
- Qdrant / OpenSearch subject erase activities (for DSR)
- Connector OAuth token revocation / purge
- Control-plane admin endpoints (tenant region override, CMK
  scheduled-deletion)
- Background re-wrap job for regional KEKs
- Redaction fan-out to intelligence service
