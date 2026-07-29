// Wave 12.2 — cross-region re-encrypt + move for content blobs.
//
// Completes the Wave 8.4 residency migration workflow: today the
// workflow flips `documents.region_pin` + emits the domain event,
// but the underlying ciphertext stays in the source region's
// bucket under the source-region KEK. This file adds the primitive
// that moves + re-keys ciphertext atomically.
//
// The operation is:
//
//  1. Load content_blobs row (tenant-scoped, RLS-protected).
//  2. GET ciphertext from source bucket.
//  3. Unwrap DEK under the source KEK alias.
//  4. Generate fresh DEK under the target region's KEK alias
//     (ADR 0026: region-scoped master + HKDF).
//  5. AES-GCM re-encrypt the plaintext with the new DEK + nonce.
//  6. PUT ciphertext into the target bucket.
//  7. UPDATE the content_blobs row with the new storage + envelope
//     fields in a tenant-scoped transaction.
//  8. DELETE the source-bucket object (only after the DB update
//     commits — if the update fails, we still own the source
//     object and can safely retry).
//
// Atomicity model: the Postgres UPDATE is the commit point. If it
// errors after the target bucket already has the ciphertext, a
// reaper-style reconciliation (Wave 12 follow-up) can detect the
// orphan and delete it. If any step before the UPDATE fails, the
// migration is a no-op — the source is untouched.
//
// Same tenant throughout; a caller attempting to migrate a blob
// owned by a different tenant hits the RLS UPDATE (0 rows
// affected) and the call errors.
package service

import (
	"bytes"
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/storage/internal/model"
)

// ReencryptBlobInput configures one migration call.
type ReencryptBlobInput struct {
	TenantID     uuid.UUID
	BlobID       uuid.UUID
	TargetRegion string
	// TargetTier defaults to "hot" when empty — matches uploads.
	TargetTier string
}

// ReencryptBlobResult is the post-migration summary.
type ReencryptBlobResult struct {
	BlobID         uuid.UUID
	SourceRegion   string
	TargetRegion   string
	SourceBucket   string
	TargetBucket   string
	TargetKey      string
	NewKEKAlias    string
	BytesRewrapped int64
}

// ReencryptBlob migrates a content blob from its current region to
// TargetRegion, re-wrapping the DEK under the target region's KEK.
// Idempotent on the happy path: calling twice with the same target
// region on an already-migrated blob returns a successful no-op
// result (same region → no work).
func (s *Service) ReencryptBlob(ctx context.Context, in ReencryptBlobInput) (*ReencryptBlobResult, error) {
	if in.TargetRegion == "" {
		return nil, vdmserr.Validation("target_region", "required")
	}
	tier := in.TargetTier
	if tier == "" {
		tier = "hot"
	}
	targetBucket := bucketName(in.TargetRegion, tier)

	// Phase 1: load the blob row (tenant-scoped).
	var blob *model.ContentBlob
	if err := database.WithTenantTx(ctx, s.cfg.Pool, in.TenantID, func(tx pgx.Tx) error {
		b, err := s.cfg.Repos.ContentBlobs.GetByID(ctx, tx, in.TenantID, in.BlobID)
		if err != nil {
			return err
		}
		blob = b
		return nil
	}); err != nil {
		return nil, fmt.Errorf("load blob: %w", err)
	}
	if blob == nil {
		return nil, vdmserr.NotFound("content blob not found")
	}

	// Refuse to re-encrypt a WORM-locked blob. Re-encryption re-writes the
	// ciphertext into an ordinary (non-object-locked) bucket, repoints the row,
	// and deletes the source object — stripping the S3 object-lock and thus WORM
	// immutability before retention expires. A retention-preserving cross-region
	// path (copy into the target WORM bucket + re-apply the lock) is tracked in
	// docs/security/epic6-storage-followups.md (#10/#5).
	if isWORMBucket(blob.StorageBucket, blob.StorageRegion) {
		return nil, vdmserr.Validation("blob", "cannot re-encrypt a WORM-locked blob; retention would be stripped")
	}

	// Idempotence: already in target region under a target-scoped
	// KEK alias → no work, return a result reflecting current state.
	targetKEK := aliasForTenantInRegion(in.TenantID, in.TargetRegion)
	if blob.StorageRegion == in.TargetRegion && blob.KEKID == targetKEK {
		return &ReencryptBlobResult{
			BlobID:       blob.ID,
			SourceRegion: blob.StorageRegion,
			TargetRegion: in.TargetRegion,
			SourceBucket: blob.StorageBucket,
			TargetBucket: blob.StorageBucket,
			TargetKey:    blob.StorageKey,
			NewKEKAlias:  blob.KEKID,
		}, nil
	}

	// Phase 2: pull ciphertext from source bucket.
	sourceRC, err := s.cfg.S3.GetObject(ctx, blob.StorageBucket, blob.StorageKey)
	if err != nil {
		return nil, fmt.Errorf("get source: %w", err)
	}
	defer sourceRC.Close()

	// Phase 3: decrypt under source KEK. decryptAll runs through
	// KeyManager.DecryptDataKey, which respects ADR 0026 region
	// scoping via the kekID (e.g. "vaultdms/tenant/<uuid>/us-east-1").
	plaintext, err := s.decryptAll(ctx, sourceRC, blob.KEKID, blob.EncryptedDEK, blob.DEKNonce)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	// Phase 4 + 5: fresh envelope under target KEK.
	reenc, err := s.encryptAll(ctx, bytes.NewReader(plaintext), targetKEK)
	if err != nil {
		// Best effort: zero the plaintext even on failure. encryptAll
		// won't have zeroed it.
		for i := range plaintext {
			plaintext[i] = 0
		}
		return nil, fmt.Errorf("re-encrypt: %w", err)
	}
	for i := range plaintext {
		plaintext[i] = 0
	}

	// Phase 6: write ciphertext to target bucket. Keep the same key
	// so tenant-prefix + year-month path stays predictable; the
	// bucket flip is all that differentiates regions.
	if err := s.cfg.S3.PutObject(
		ctx, targetBucket, blob.StorageKey,
		bytes.NewReader(reenc.Ciphertext), int64(len(reenc.Ciphertext)), blob.MimeType,
	); err != nil {
		return nil, fmt.Errorf("put target: %w", err)
	}

	// Phase 7: UPDATE the blob row atomically.
	updated := *blob
	updated.StorageRegion = in.TargetRegion
	updated.StorageBucket = targetBucket
	updated.EncryptedDEK = reenc.EncryptedDEK
	updated.DEKNonce = reenc.Nonce
	updated.KEKID = reenc.KEKID
	if err := database.WithTenantTx(ctx, s.cfg.Pool, in.TenantID, func(tx pgx.Tx) error {
		return s.cfg.Repos.ContentBlobs.UpdateMigration(ctx, tx, &updated)
	}); err != nil {
		// Orphaned ciphertext in the target bucket — logged so the
		// Wave 12 reconciliation job finds it. Source blob is
		// still intact, so we're safe to surface the error and
		// let the caller retry.
		s.log.Error().
			Err(err).
			Str("orphan_bucket", targetBucket).
			Str("orphan_key", blob.StorageKey).
			Str("blob_id", blob.ID.String()).
			Msg("reencrypt: db update failed; target ciphertext orphaned")
		return nil, fmt.Errorf("update blob: %w", err)
	}

	// Phase 8: remove the source ciphertext only after commit.
	// Skip when bucket didn't change (single-region MinIO dev) —
	// deleting the now-live object would break the migration.
	if blob.StorageBucket != targetBucket {
		if err := s.cfg.S3.DeleteObject(ctx, blob.StorageBucket, blob.StorageKey); err != nil {
			// The migration logically succeeded: the blob row points
			// at the new ciphertext. Leaking the old one is a GC
			// concern, not a correctness one. Log and continue.
			// SECURITY NOTE (Epic 6 #7, TRACKED): "reaper will retry" is
			// currently FALSE — BlobReaper only enumerates zero-ref content_blobs
			// rows, and after UpdateMigration no row references this stale source
			// object, so nothing ever reaps it. The old ciphertext (under the OLD
			// KEK) persists indefinitely, weakening crypto-shred/residency intent.
			// Needs an orphan-S3 reconciliation job. See
			// docs/security/epic6-storage-followups.md.
			s.log.Warn().
				Err(err).
				Str("stale_bucket", blob.StorageBucket).
				Str("stale_key", blob.StorageKey).
				Msg("reencrypt: source cleanup failed; object orphaned (no reconciliation yet)")
		}
	}

	return &ReencryptBlobResult{
		BlobID:         blob.ID,
		SourceRegion:   blob.StorageRegion,
		TargetRegion:   in.TargetRegion,
		SourceBucket:   blob.StorageBucket,
		TargetBucket:   targetBucket,
		TargetKey:      blob.StorageKey,
		NewKEKAlias:    reenc.KEKID,
		BytesRewrapped: int64(len(reenc.Ciphertext)),
	}, nil
}
