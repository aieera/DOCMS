# Epic 6 — storage service: fixes + deferred follow-ups

The Epic 6 adversarial review of the storage service (envelope encryption /
DEK-KEK, crypto-shred reaper, virus scan, tenant isolation, WORM) confirmed 12
defects (5 high). This branch fixed the clean, high-value ones and added
pragmatic fail-closed guards; the remainder are deferred with an in-code
`SECURITY NOTE (Epic 6 #N, TRACKED)` because a correct fix needs a schema
migration, a URL-lifecycle change, a reconciliation job, or caller-identity
plumbing that is unsafe to bolt on mid-refactor.

## Fixed on this branch
| # | Sev | What |
|---|-----|------|
| 1 | HIGH | Reaper now deletes the DB row FIRST (guarded on `reference_count=0`) and only then the S3 object. A blob re-referenced via dedup during the grace window keeps its bytes instead of ending up a live row pointing at a destroyed object (silent data loss). |
| 4 | HIGH | Reaper skips WORM-bucket blobs — hard-deleting the row would destroy the only wrapped-DEK copy and crypto-shred a still-retained blob before expiry. |
| 10 | HIGH | `ReencryptBlob` refuses a WORM-locked blob — re-encrypting into a plain bucket would strip S3 object-lock (WORM) before retention expiry. |
| 2 | HIGH | Per-tenant upload allowlist re-enforced at CompleteUpload against the magic-byte-detected MIME (was only checked at initiate against client-declared metadata → trivially bypassable). |
| 8 | MED | `enforceUploadPolicy` fails CLOSED on a policy-store read error (was fail-open → allowlist silently skipped on any transient DB error). |
| 6 | MED | `aliasForTenant(uuid.Nil)` returns "" so the KMS fails closed, instead of `vaultdms/tenant/nil` which every manager derived into one shared, predictable KEK (defeating per-tenant crypto-shred). |
| 12 | LOW | The three privileged internal endpoints (WORM / rewrap / reencrypt) now constant-time-compare the shared internal key (`internalServiceKeyOK`), closing a timing side channel. |

WORM guards #4/#10 use `isWORMBucket` (storage_bucket == `dms-<region>-worm`) as the
only in-DB retention signal available today — see #5 for the proper fix.

## Deferred (tracked)
| # | Sev | Site | Why deferred / remediation |
|---|-----|------|----------------------------|
| 5 | HIGH | `service/worm.go` `ApplyWORM` | Retention (retain_until/mode/legal_hold) is persisted ONLY as S3 object-lock metadata, never in `content_blobs`, so app paths can't fail-closed on retained blobs. This is the root cause behind #4/#10. Proper fix: add DB retention columns (migration) + have reaper/reencrypt/rewrap consult them. The #4/#10 bucket-name guards are the interim mitigation. |
| 3 | HIGH | `service/service.go` presigned PUT | The presigned PUT URL is valid for the full `UploadTTL` and is never invalidated at CompleteUpload; with `EncryptAtRest=false` (no server re-write) this is a swap-after-clean-scan TOCTOU. Fix: single-use / very-short-TTL the PUT URL, or re-hash on download. (EncryptAtRest=true already defeats it via fresh-DEK AEAD.) |
| 7 | MED | `service/reencrypt.go` phase 8 | On source-delete failure the old ciphertext (old KEK) is orphaned forever — the "reaper will retry" claim is false (reaper only sees zero-ref rows, none reference the stale object). Needs an orphan-S3 reconciliation sweep. |
| 9 | MED | `service/service.go` `GetDownloadURL` | Presigns a GET for any completed upload in the tenant with no per-user read/ownership check (unlike the upload path). Trusts the calling document service. Add defense-in-depth `CheckPermission(view, document)` once caller identity + version→upload resolution are wired (method is transitional). |
| 11 | LOW | `service/service.go` `AbortUpload` / `GetScanStatus` | Tenant-scoped only, no per-user check — any tenant user can abort another's in-flight upload / read its scan status by id. Same defense-in-depth plumbing as #9. |
