# ADR 0030 — Saved signature profile key wrap + crypto-shred

* Status: Accepted
* Date: 2026-04-21
* Wave: 15.4

## Context

Wave 15.4 lets a user save a signature (drawn on a canvas, uploaded, or typed with a preset font) so the PAdES envelope flow can reuse it without redrawing. Requirements:

1. Images are PII-adjacent (a facsimile of a handwritten signature). Must encrypt at rest with per-tenant KEK.
2. Deleting a profile must be **crypto-shred** — the image MUST be unrecoverable from any backup or replication lag, not merely soft-deleted.
3. The PAdES cryptography of Wave 9 is untouched — saved signatures only supply the visual appearance, never the signing key.
4. One user may have many profiles; deleting one profile must NOT expose the rest.

## Decision

### Per-profile DEK, tenant-KEK-wrapped

On `CreateProfile`:

1. Generate a fresh 32-byte DEK via `crypto.KeyManager.GenerateDataKey(ctx, kekID)`.
2. `kekID = "vaultdms/tenant/<tenantID>/signature-profiles"` — namespaced per ADR 0022 so it cannot collide with document / MFA / acknowledgement-attestation keys.
3. AES-GCM encrypt the image with the plaintext DEK. Persist `(ciphertext @ S3, nonce, wrapped_dek, kek_id)` on the row.
4. Plaintext DEK is zeroed before return (`zeroBytes`) — best-effort defence-in-depth; Go's runtime can move buffers, so this isn't a hard guarantee.

Every profile has its own DEK. Compromising one DEK does not expose any other profile's plaintext.

### Crypto-shred on delete

Deletion is three steps, each separately durable:

1. `Revoke` — sets `revoked_at = now()` + `is_default = false`. This is a soft-delete inside a tenant tx so a user's `List` call immediately sees the profile gone.
2. `S3 DeleteObject` — removes the ciphertext at `ImageRef`. After this, the AES-GCM ciphertext is gone from object storage.
3. `HardDelete` — removes the row, taking `wrapped_dek` + `nonce` with it.

After step 3, reconstructing the image requires either (a) restoring the row from a backup (which also brings back `wrapped_dek`) AND the ciphertext from its own backup, OR (b) cracking AES-256-GCM. The "crypto-shred" label hinges on per-profile DEK uniqueness: we do NOT need to rotate the tenant KEK on delete — we just need to make the row's `wrapped_dek` unrecoverable, which row-level deletion accomplishes.

Backup policies (Postgres PITR, S3 versioning) must be set such that after the retention window expires, both the row and the ciphertext are truly gone. This is documented in the runbook.

### Service never trusts client pixels at signing time

Wave 9's PAdES envelope flow already runs server-side. When the frontend renders the "use saved signature" dropdown, it sends only the profile `id` to the signing backend. The backend fetches + decrypts the image via `ProfileService.ResolveImage` and composes it into the visual appearance layer. A client that POSTs raw pixels is rejected — the signing path has no API for it.

This closes a class of bugs where a compromised frontend substitutes the image the user thought they were signing with.

## Consequences

**Positive**
- Each profile's plaintext is recoverable only by unwrapping its own DEK, which lives solely on the row. Cross-profile isolation is automatic.
- Delete is a real crypto-shred: no "secret" survives row-deletion + S3-deletion + backup-retention expiry.
- Server-side image composition makes the "saved signature" pathway as trustworthy as the first-time-draw pathway.

**Negative**
- Extra `GenerateDataKey` call per profile — for a user who saves 3 profiles, that's 3 KMS round-trips. Acceptable for a rarely-exercised path (profile creation is once-per-user-per-signature-style).
- Partial failure window: if S3 DeleteObject succeeds but `HardDelete` fails, the row is orphaned (soft-deleted, no object). A sweeper that finds rows with `revoked_at IS NOT NULL AND created_at < now() - interval '7 days'` is tracked as a Wave 15.4 follow-up.
- `zeroBytes` is advisory — Go runtime may have copied the DEK before the wipe. Swapping `crypto/subtle`'s constant-time primitives for deletion is out of scope.

**Neutral**
- The `font_style` column is only populated for `kind='typed'`; storing it on the same row rather than a dedicated table trades minor sparsity for simpler queries.

## Alternatives considered

1. **Shared tenant-level DEK for all profiles**. Simpler but doesn't meet the "deleting one must not expose others" property after KEK rotation + partial key compromise. Rejected.
2. **Store plaintext image, rely on disk encryption**. Fails the threat model — a leaked DB dump would expose signatures. Rejected.
3. **Sign the image itself with the user's signing cert**. Confuses the visual appearance layer with the cryptographic signature — the two are supposed to be independent per PAdES. Rejected.
4. **Use a Merkle tree of profile images for verification**. Overengineered — there's no multi-profile correlation attack to defend against. Rejected.

## Follow-ups

- Sweeper for orphaned S3 objects + revoked rows.
- `dms-admin signature-profile export --user <id>` for DR / user-data-export (GDPR-DSR aligned).
- Presigned-get URL for the `/image` endpoint (today streams through the service); requires a signed-decrypt proxy.
- Frontend `/settings/signatures` page + "Use saved signature" dropdown in the envelope signing flow.
