# 0022 — Per-tenant KEK derivation strategy

- **Status:** Accepted
- **Date:** 2026-04-17
- **Deciders:** Wave 6 Prompt 6.1 execution
- **Supersedes:** —

## Context

Before this ADR, every tenant's object-encryption DEK was wrapped by a
single shared KEK (`vaultdms-storage-default`). Blast radius of a
single key compromise = every document ever uploaded. This is a pilot
blocker (final.md § 2.4).

Three deployment modes need covering:

1. **Dev / CI** — no cloud KMS, single-process test containers. Must
   produce deterministic KEKs so restarts don't invalidate ciphertext.
2. **On-prem / air-gapped** — no cloud KMS by definition; customer may
   run HashiCorp Vault, a hardware HSM, or nothing at all.
3. **SaaS** — AWS KMS / GCP KMS / Azure Key Vault; each has native
   per-alias isolation.

The existing `KeyManager` interface already accepts `kekID` in every
method, so the call sites are ready. The bug is that
`LocalKeyManager` ignores the parameter.

## Decision

Introduce a **per-tenant KEK alias** of the form
`vaultdms/tenant/<tenant_uuid>[@vN]` (version suffix optional for the
live version; explicit for historical lookups). Record aliases in a
new `tenant_keks` table; one live row per tenant enforced by a
partial unique index.

Each KeyManager implementation resolves the alias differently:

- **LocalKeyManager (dev/CI):** derives the tenant KEK as
  `HKDF-SHA256(VAULTDMS_LOCAL_KEK, salt="kek/"+alias, info="vaultdms/v1", L=32)`.
  HKDF guarantees that knowing one tenant's KEK does not help recover
  another's. The master secret never leaves memory; it is not persisted
  and not reachable from SQL.
- **VaultKeyManager:** looks up the alias in Vault Transit under
  `transit/keys/<alias>`. Rotation = `vault write transit/keys/<alias>/rotate`.
- **AWSKMSKeyManager:** resolves the alias via
  `alias/vaultdms-tenant-<uuid>`, which points to a CMK. Rotation =
  `kms scheduleKeyDeletion` on the previous CMK plus creation of a new
  one; application records the new alias in `tenant_keks`.

Storage writes the alias that was used (in `content_blobs.kek_id`) so
a decrypt knows which KEK to fetch — independent of the currently-
live alias.

### Non-goals

- **Live re-encryption of historical objects** is NOT mandated by
  this ADR. Retired KEK rows stay in the table and continue to
  service decrypts. A separate admin workflow (`dms-admin kms rotate
  --tenant <id> --migrate`) can re-wrap DEKs online — that is Wave 6
  follow-up work, not this prompt.

## Consequences

**Easier**
- Cross-tenant data extraction via KEK compromise now requires N
  KEKs, one per tenant.
- On-prem deployment needs no cloud KMS to get the isolation
  benefit — HKDF derivation is pure crypto on a single root secret.
- The existing `GenerateDataKey(ctx, kekID)` call site in storage
  service keeps working unchanged once the caller passes the right
  alias.

**Harder**
- The master secret `VAULTDMS_LOCAL_KEK` becomes load-bearing: losing
  it makes every tenant's objects unrecoverable in dev/on-prem mode.
  Operators must rotate via explicit `dms-admin secrets rotate` and
  run the lazy re-wrap after — Wave 6 follow-up.
- Dev/CI backups MUST include the master secret or tests against
  restored DBs will fail decrypt. Documented in
  `docs/runbooks/06-key-management.md`.

**Neutral**
- RLS on `tenant_keks` prevents one tenant's admin from listing
  another tenant's alias set.

## Alternatives considered

1. **Per-tenant cloud KMS CMK only (no local derivation).**
   Rejected for dev/CI friction — every test run would need a live
   KMS, or the test suite would have to mock it. Current approach
   works without any dev infrastructure.

2. **Per-tenant random KEK stored in `tenant_keks.material` (BYTEA).**
   Rejected on principle: never store the KEK in the same DB that
   holds the ciphertext. The HKDF approach keeps the master secret
   out of DB and out of operator shells.

3. **PBKDF2 or Argon2id instead of HKDF.**
   Rejected: those are password-hardening primitives, not key
   derivation. HKDF is the RFC 5869 construction designed exactly for
   this (high-entropy master → many subkeys).

## Sources

- RFC 5869 — HMAC-based Extract-and-Expand Key Derivation Function (HKDF).
- NIST SP 800-57 Part 1 Rev. 5 (cryptographic key management guidance).
- `DMS Architecture/final.md` § 2.4, § 5.1.
- `pkg/crypto/kms.go` — existing KeyManager interface.
- `services/storage/internal/service/service.go:110` — the `vaultdms-storage-default` line that this ADR removes.
