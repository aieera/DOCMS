# 0026 — Per-region KEK masters

- **Status:** Accepted
- **Date:** 2026-04-18
- **Supersedes:** —
- **Deciders:** core eng + security

## Context

Wave 6.1 / ADR 0022 introduced per-tenant KEK derivation via
HKDF-SHA256 from a single master secret:

```
tenantKEK = HKDF(ikm=master, salt="vaultdms/kek/vaultdms/tenant/<uuid>",
                 info="vaultdms/kek/v1")
```

Wave 8.4 shipped the residency dashboard + migrate-documents
workflow: administrators can now pin documents to a specific region
and migrate them between regions. Ciphertext moves, but the
encryption stays under the same single-master HKDF derivation
regardless of region.

This breaks a compliance invariant EU customers explicitly asked
about: a US-region breach (leaked master, Vault/KMS compromise,
rogue operator) today decrypts EU-region ciphertexts too, because
all regions share one ikm. The HKDF salt includes the region in
the kekID but salt doesn't isolate under a compromised ikm.

## Decision

**Introduce a per-region master-secret map.** `LocalKeyManager`
grows a `regionMasters map[string][]byte` field. kekIDs of the form
`vaultdms/tenant/<uuid>/<region>` select the master for `<region>`;
older ids without the suffix continue to use the global master for
backwards compatibility.

A kekID claiming a region that isn't configured is a hard error —
no silent fallback. This matters because a silent fallback would
re-encrypt EU documents under the US master without anyone
noticing.

### Alias format

Current (pre-11.7) alias: `vaultdms/tenant/<uuid>`.
Post-11.7 regional alias: `vaultdms/tenant/<uuid>/<region>`.
Rotation suffix still honoured: `vaultdms/tenant/<uuid>/<region>@v2`.

`services/storage/internal/service/tenant_kek.go` gained
`aliasForTenantInRegion(tenantID, region)`. Callers that operate
per-region (upload, presigned GET) resolve the region from
`content_blobs.storage_region` or `documents.region_pin` and pass
it in.

### Wire-through

Production adapters (Vault / AWS KMS — still stubs per ADR 0025)
will follow the same alias format. Vault's Transit engine already
supports per-key isolation via its own key name scheme; AWS KMS
supports per-region CMKs natively. The shared contract is: the
provider MUST refuse to decrypt a ciphertext wrapped under region
A using a region-B KEK. LocalKeyManager enforces this via the
hard error on unknown region + distinct HKDF salts.

## Consequences

- **Easier.** EU customers get a straight answer to "does a US
  breach expose EU data?" — no.
- **Harder.** Operators now provision and rotate N master secrets
  per deployment (N = number of regions). Runbook
  `docs/runbooks/06-key-management.md` will document the rotation
  order: rotate regional masters independently; the global master
  is used only for pre-11.7 legacy data and gets retired once a
  one-time re-wrap job migrates those DEKs.
- **Migration of existing data.** Every DEK wrapped under the
  global master keeps its `vaultdms/tenant/<uuid>` kekID. A
  background re-wrap job (not in this wave) reads each blob's
  region_pin and re-wraps into `vaultdms/tenant/<uuid>/<region>`.
  Until it runs, reads for legacy blobs fall back to the global
  master automatically.
- **Accepted compromise.** `LocalKeyManager` holds every region's
  master in one process's memory; a process-level compromise
  (core dump, debugger) leaks all regions. The production Vault /
  AWS KMS adapters mitigate this at the KMS boundary — neither
  region's master leaves the HSM / KMS there.

## Alternatives considered

- **Single master + region-in-salt only** (the Wave 8.4 status
  quo). Pros: zero operator work. Cons: doesn't isolate against
  ikm compromise. Rejected.
- **Derive regional master from global via HKDF with region as
  salt.** Pros: one secret to rotate. Cons: same ikm compromise
  model — breaking the global master breaks all regions.
  Rejected.
- **Require separate `LocalKeyManager` instances per region and
  route by region at the call site.** Pros: no intra-manager
  branching. Cons: proliferates instances across every
  storage-service handler; each cache flight independently. The
  single-instance map was simpler with no meaningful security
  downside.

## Sources

- Wave 6.1 Remediation 13a (ADR 0022 per-tenant KEK derivation).
- Wave 8.4 Remediation 15d (residency migrate workflow).
- RFC 5869 HKDF — salt ≠ ikm isolation.
- AWS KMS multi-region key docs.
- HashiCorp Vault Transit engine key-per-name semantics.
