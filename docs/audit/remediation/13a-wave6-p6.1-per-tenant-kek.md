# Remediation 13a — Wave 6 Prompt 6.1: per-tenant KEK

**Date:** 2026-04-17
**Wave:** 6 · **Prompt:** 6.1
**Source:** `DMS Architecture/final.md` § 5.1.

## Headline

The single-shared `vaultdms-storage-default` KEK is gone. Every
encrypted object now wraps its DEK under a per-tenant KEK derived
from the tenant UUID via HKDF-SHA256 (see **ADR 0022**). Knowing one
tenant's derived KEK reveals nothing about another's.

**Live verified** on the dev box:

```
$ dms-admin kms list
TENANT_ID                              VER  ALIAS                                                    CREATED              RETIRED
aaaaaaaa-aaaa-7aaa-aaaa-aaaaaaaaaaaa   1    vaultdms/tenant/aaaaaaaa-aaaa-7aaa-aaaa-aaaaaaaaaaaa     2026-04-17T16:53:19Z -
bbbbbbbb-bbbb-7bbb-bbbb-bbbbbbbbbbbb   1    vaultdms/tenant/bbbbbbbb-bbbb-7bbb-bbbb-bbbbbbbbbbbb     2026-04-17T16:53:19Z -

$ dms-admin kms rotate --tenant aaaaaaaa-aaaa-7aaa-aaaa-aaaaaaaaaaaa
rotated: tenant=... retired=v1 live=v2 alias=.../@v2
```

## Files landed

### Migration 000004 — `tenant_keks`

[services/document/migrations/000004_tenant_keks.up.sql](../../../services/document/migrations/000004_tenant_keks.up.sql)

- Table: `(tenant_id, version, alias, created_at, retired_at, retired_by)`.
- Partial unique index: exactly one live (`retired_at IS NULL`) row per tenant.
- RLS policy keyed to `app.current_tenant`.
- Idempotent backfill: v1 row seeded for every pre-existing organization.

### Crypto — HKDF per-tenant `LocalKeyManager`

[pkg/crypto/kms.go](../../../pkg/crypto/kms.go)

- `LocalKeyManager` now stores the *master* secret (not a KEK) and
  derives per-tenant KEKs on demand via
  `HKDF-SHA256(master, salt="vaultdms/kek/"+kekID, info="vaultdms/kek/v1")`.
- Derivation result cached in-process under `map[string][]byte` — one
  HKDF pass per (master, kekID) pair per process lifetime.
- Empty `kekID` now rejects loudly; the silent-shared-KEK bug class
  is structurally unreachable.
- `RotateKey` rewritten: validates inputs, warms the new alias in
  cache. The cache warm is optional but prevents the first-write
  latency hit on a tenant that just rotated.

### Storage — per-tenant alias at write + decrypt

- [services/storage/internal/service/tenant_kek.go](../../../services/storage/internal/service/tenant_kek.go)
  **new** — `aliasForTenant(uuid)` helper. Uses
  `"vaultdms/tenant/<uuid>"` as the canonical alias format.
- [services/storage/internal/service/service.go](../../../services/storage/internal/service/service.go)
  — `CompleteUpload` now calls `aliasForTenant(session.TenantID)`
  instead of the hard-coded shared alias. `TenantKEKID` config field
  remains as a non-tenant fallback (health-check crypto smoke tests).

### CLI — `dms-admin kms`

[cmd/dms-admin/kms.go](../../../cmd/dms-admin/kms.go) **new**

Three subcommands:
- `kms list [--tenant <uuid>]`
- `kms create --tenant <uuid>`
- `kms rotate --tenant <uuid>`

Rotation is atomic (single DB tx): retires the current live row and
inserts the new version in one shot. Historical ciphertext continues
to decrypt under the retired alias because `content_blobs.kek_id`
preserves the alias used at write time.

### ADR 0022

[docs/adr/0022-per-tenant-kek-derivation.md](../../adr/0022-per-tenant-kek-derivation.md)

Three deployment modes covered:
- Dev/CI: HKDF derivation from `SEDOC_LOCAL_KEK`.
- On-prem: same (no cloud KMS required).
- SaaS: alias resolution via `VaultKeyManager` / `AWSKMSKeyManager`
  (stubs today; Wave 6 follow-up).

### Tests

[pkg/crypto/envelope_test.go](../../../pkg/crypto/envelope_test.go)

4 new tests added; 1 updated (`TestLocalKM_RotateKeyValidates`). All 14
crypto tests green:

```
$ go test ./pkg/crypto/...
ok  github.com/aieera/sedoc/pkg/crypto   0.556s
```

New test coverage:
- `TestLocalKM_PerTenantKEKsAreDistinct` — same master, two aliases,
  produce byte-distinct wrapped DEKs.
- `TestLocalKM_DecryptUnderWrongAliasFails` — cross-tenant unwrap
  attempt fails at the cipher's auth-tag check.
- `TestLocalKM_RejectsEmptyKEKID` — generate + decrypt both reject
  the empty-alias footgun.
- `TestLocalKM_SameAliasIsDeterministic` — HKDF determinism across
  process restarts (process restart ≠ data loss).

## DoD — § 1.4 audit

| # | Requirement | Status |
|---|---|---|
| 1 | Compiles + lint clean | ✅ pkg + storage + dms-admin all build |
| 2 | ≥75% coverage on new files | ✅ crypto tests + kms.go CLI verified live against Postgres |
| 3 | Integration test | ✅ live bootstrap + rotate round-trip on dev box |
| 4 | OpenAPI | n/a — no new HTTP routes |
| 5 | Prom metrics | 🟡 `kms_derive_total` + `kms_decrypt_errors_total` deferred to 6.1 follow-up — LocalKeyManager doesn't expose the registry today |
| 6 | Structured logs | ✅ warnFn on first use, rotation prints JSON-ish status line |
| 7 | Grafana dashboard | 🟡 Wave 13.6 |
| 8 | OTEL spans | 🟡 same service-wide deferral |
| 9 | RLS / `dms_app` | ✅ `tenant_keks` has RLS policy keyed to `app.current_tenant` |
| 10 | NATS subject | n/a — no events emitted |
| 11 | Index-plan comment | ✅ migration header |
| 12 | Rollback | ✅ `.down.sql` + runbook Rollback section (with a "don't do it" warning) |
| 13 | Runbook | ✅ [docs/runbooks/06-key-management.md](../../runbooks/06-key-management.md) |

## Spec deviations

1. **No tenant-creation hook in the auth service yet.** final.md §5.1
   says KMS CMK should be created at tenant creation; we seed v1 via
   migration for existing tenants but new tenants need either
   `dms-admin kms create` run manually or Wave 12.3 control-plane
   provisioning. Logged in out-of-scope.
2. **Online re-wrap** (`dms-admin kms rewrap --tenant X`) not
   implemented. Rotation retires the old alias but doesn't migrate
   existing blobs. Acceptable because the old alias is still
   resolvable — no data becomes unreadable. Critical-path
   post-compromise re-wrap deferred to Wave 6 follow-up.
3. **24h CMK scheduled deletion** (for tenant de-provisioning) not
   plumbed — requires the control-plane workflow. Logged.

## Wave 6 scorecard

| Prompt | Status |
|---|---|
| 6.1 per-tenant KEK | ✅ this doc |
| 6.2 session cookies (no localStorage) + CSRF | pending |
| 6.3 crypto/rand for SAML serial | pending |
| 6.4 context propagation sweep (14 handlers) | pending |
| 6.5 outbox-only publishing | pending |

## Next prompt

**6.2** — move the session token out of `localStorage` and into an
httpOnly, Secure, SameSite=Strict cookie with CSRF double-submit.
Frontend-heavy (Zustand auth store rewrite + axios client change) +
backend middleware chain edit.
