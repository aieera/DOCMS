# Remediation 19c — Wave 12.3: Background re-wrap CLI for regional KEKs

**Date:** 2026-04-18
**Wave:** 12.3 · completes ADR 0026 §Consequences "background re-wrap" follow-up.

## Recon

Wave 11.7 / ADR 0026 introduced region-scoped kekIDs
(`vaultdms/tenant/<uuid>/<region>`). Wave 12.2 built the primitive
that re-keys ciphertext from the legacy alias to the regional one
(`ReencryptBlob`). Neither wave provided the operator-facing
migration loop — legacy data with pre-regional `kek_id` stayed
decryptable only via the global master.

## What shipped

### New CLI subcommand

[cmd/dms-admin/kms_rewrap_regional.go](../../../cmd/dms-admin/kms_rewrap_regional.go):

```
dms-admin kms rewrap-regional --tenant <uuid> [--region <id>] [--limit N] [--execute]
```

Flow:

1. **Enumerate blobs** whose `kek_id` lacks a region suffix. The
   detection regex is `/[^/]+(@v[0-9]+)?$` — regional aliases end
   with `/<region>` or `/<region>@v<N>`; anything that doesn't
   match is legacy.
2. **Join to documents** to surface each blob's target region via
   `documents.region_pin`. `--region` overrides per run.
3. **Print** a three-column table: blob id, current region, target
   region. Caps at `--limit` (default 1000) so operators can
   resume between batches.
4. **Dry run by default.** `--execute` prints the exact
   `POST /internal/v1/reencrypt-blob` payload the storage-service
   endpoint (Wave 12.3b) will accept. No re-encryption happens
   from the CLI itself — that lives in the storage service where
   KMS + S3 are already wired.

Idempotence flows from `ReencryptBlob`'s same-region-same-alias
no-op branch (Wave 12.2) — a partially-completed run can restart
safely without re-wrapping already-migrated blobs.

### CLI hook

[cmd/dms-admin/kms.go](../../../cmd/dms-admin/kms.go) registers
the `rewrap-regional` subcommand alongside `list / create /
rotate`. Usage help updated.

## DoD

| Requirement | Status |
|---|---|
| Enumerate legacy blobs | ✅ regex detection of missing region suffix |
| Resumable | ✅ `--limit`; re-run picks up remaining rows |
| Idempotent | ✅ `ReencryptBlob` no-op branch fires for already-regional blobs |
| Operator-visible output | ✅ blob id + current + target columns |
| Dry-run default | ✅ |

## Deferred (logged in out-of-scope.md)

- **Storage-service admin HTTP endpoint** `POST /internal/v1/reencrypt-blob`
  that the CLI posts to under `--execute`. Wave 12.3b.
- **Progress file** so `--limit` interrupts can resume from the
  last-seen blob id without redoing the join. For tenants with
  millions of blobs this matters; for pilot-scale tenants the
  simple limit-N approach is fine.
- **Per-region summary output** — currently prints one row per
  blob. A `--summary` flag that aggregates (`5,213 blobs in
  us-east-1 → eu-west-1`) would shorten operator-facing output.

## Wave 12 scorecard

| Item | Status |
|---|---|
| 12.1 SMTP transactional email | ✅ |
| 12.2 Storage cross-region re-encrypt | ✅ |
| **12.3 Background re-wrap CLI** | ✅ this doc |
| 12.4 Qdrant / OpenSearch subject erase | pending |
| 12.5 Redaction fan-out to intelligence | pending |
| 12.6 Connector OAuth token purge | pending |
| 12.7 Control-plane admin endpoints | pending |
| 12.8 Vault / AWS KMS production adapters | pending |
| 12.9 DSS Java sidecar | pending |

## Next prompt

**12.4 — Qdrant / OpenSearch subject erase activities.** DSR erase
(Wave 8.3 + 11.6) scrubs every user-keyed Postgres table but
leaves the subject's data in Qdrant vector payloads and
OpenSearch document-body indexes. Each needs a Temporal activity
that the erase workflow dispatches in fan-out.
