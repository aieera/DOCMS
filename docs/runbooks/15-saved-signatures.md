# Runbook 15 — Saved signature profiles

**Owner:** Signature Engineering · **Last rehearsed:** *(fill on next on-call tabletop)*

## What it does
Wave 15.4 lets a user store one or more signature profiles (drawn / uploaded / typed) and pick one when signing a PAdES envelope. Profiles are encrypted at rest with per-profile DEKs wrapped under the tenant KEK. PAdES cryptography is unchanged.

## Key surfaces
| Endpoint | Purpose |
|---|---|
| `GET    /api/v1/signatures/profiles` | Caller's non-revoked profiles |
| `POST   /api/v1/signatures/profiles` | Create (JSON body with base64 image) |
| `PATCH  /api/v1/signatures/profiles/{id}` | Rename or set-default |
| `DELETE /api/v1/signatures/profiles/{id}` | Crypto-shred |
| `GET    /api/v1/signatures/profiles/{id}/image` | Decrypted bytes (owner-only) |

## Known gotchas
1. **1 MiB image cap.** The service rejects images larger than `ProfileMaxBytes`. Canvas PNG drawings typically weigh in at 5–50 KiB, so the cap is defensive — anything bigger is almost always a misuse (user uploaded a scan of a document, not a signature). If a legitimate use case hits the cap, raise it in code + run a data-migration-less redeploy.
2. **Crypto-shred is final.** `DELETE /profiles/{id}` revokes → deletes the S3 object → hard-deletes the row. Recovery after backup-retention expiry is impossible by design. If a user "accidentally deleted", the answer is "create a new profile".
3. **Orphan S3 objects on partial failure.** If S3 DeleteObject succeeds but `HardDelete` fails (pg down, etc.) the row is left with `revoked_at` set and no object. A sweeper that cleans these up is a Wave 15.4 follow-up — until it ships, a periodic manual query is the workaround:
   ```sql
   SELECT id, tenant_id, user_id FROM signature_profiles
    WHERE revoked_at IS NOT NULL AND revoked_at < now() - interval '1 day';
   ```
4. **Default swap is atomic per user.** The service flips the previous default off in the same tx that sets the new one. The partial unique index guarantees the invariant even if a race sneaks through.
5. **Image endpoint streams plaintext.** There's no presigned-URL path today — the service decrypts and writes the bytes directly. `Cache-Control: private, no-store` is set so proxies don't cache; ensure any CDN in front respects it.
6. **SSO users use profiles too.** There's no "password" constraint — any authenticated user can create and reuse profiles regardless of auth method.

## Symptoms → fixes

### User sees "forbidden" on their own profile image
Check `signature_profiles.user_id` vs the session's user_id. Common cause: profile was created under a previous SSO identity (email-to-id changed) and `user_id` doesn't match the current session. Manual fix: point the stray row at the new user via an admin-audit SQL operation (and record why).

### Profile image comes back blank / distorted after signing
The image bytes are stored verbatim; distortion is almost always a frontend rendering issue. To rule out server-side corruption, decrypt via `dms-admin signature-profile fetch --id <uuid>` (tracked as follow-up — today use a one-off `psql` + the envelope primitives).

### KEK rotation failed mid-flight
The tenant KEK-rotation path re-wraps DEKs per tenant. If the wrapped_dek column is half-new-KEK / half-old-KEK after a rotation abort, the service falls back to the stored `kek_id` per row — no service outage. A reconciliation job walks `signature_profiles` and re-wraps stragglers; tracked as a standing Wave 11 runbook.

## Forensics: "did the user really sign with this profile?"
The saved profile's image is ONLY the visual appearance. The cryptographic PAdES signature was produced by the signer sidecar using the user's signing cert (Wave 9). Verify:

1. Extract the signature from the PDF: `dms-admin signatures verify <pdf>`.
2. Check the signer cert + chain (this is the authoritative answer to "who signed?").
3. Confirm the saved-profile image that was embedded matches the referenced profile id by re-rendering: fetch `/profiles/{id}/image`, compare bytes.

Point 1 is the audit-grade answer; points 2-3 are "cosmetic sanity".

## Backout
`000001_signature_profiles.down.sql` drops the table. All profiles (and their wrapped DEKs) are lost — the S3 objects become crypto-opaque orphans. Run **only** after exporting anything the compliance team cares about. Users can recreate profiles cheaply.
