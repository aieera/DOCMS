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
//  5. AES-GCM re-encrypt the plaintext with the new DEK + nonce, then
//     VERIFY the new envelope decrypts back to the same plaintext.
//  6. PUT ciphertext into the target bucket under a FRESH key — never
//     over the live object, even when the bucket doesn't change (D-15).
//  7. UPDATE the content_blobs row with the new storage + envelope
//     fields (bucket/key flip included) in a tenant-scoped transaction.
//  8. DELETE the old object only after the DB update commits.
//
// Atomicity model: the Postgres UPDATE is the commit point. Until it
// commits, the row and the old object are untouched and consistent, so
// any failure before or at the UPDATE leaves the blob readable exactly as
// before; the not-yet-referenced new object is deleted best-effort. The
// old object is only removed once nothing references it.
//
// Same tenant throughout; a caller attempting to migrate a blob
// owned by a different tenant hits the RLS UPDATE (0 rows
// affected) and the call errors.
package service

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"strings"

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
		zero(plaintext)
		return nil, fmt.Errorf("re-encrypt: %w", err)
	}
	// Round-trip verify BEFORE anything is written: the new envelope must
	// decrypt back to the exact plaintext, so a KMS/wrap bug fails closed
	// here instead of producing an undecryptable blob.
	check, err := s.decryptAll(ctx, bytes.NewReader(reenc.Ciphertext), reenc.KEKID, reenc.EncryptedDEK, reenc.Nonce)
	verified := err == nil && bytes.Equal(check, plaintext)
	zero(check)
	zero(plaintext)
	if !verified {
		if err == nil {
			err = fmt.Errorf("plaintext mismatch")
		}
		return nil, vdmserr.Internal("re-encrypt verification failed (new envelope does not round-trip): " + err.Error())
	}

	// Phase 6: write ciphertext under a FRESH key. Overwriting the live
	// object (same bucket + key, i.e. a same-region re-key) before the
	// row flips left a failed UPDATE pointing at the old DEK over the new
	// ciphertext — permanently undecryptable (D-15).
	targetKey := reencryptedKey(blob.StorageKey, uuid.Must(uuid.NewV7()))
	if err := s.cfg.S3.PutObject(
		ctx, targetBucket, targetKey,
		bytes.NewReader(reenc.Ciphertext), int64(len(reenc.Ciphertext)), blob.MimeType,
	); err != nil {
		return nil, fmt.Errorf("put target: %w", err)
	}

	// Phase 7: UPDATE the blob row atomically — storage location and
	// envelope flip together, compare-and-set on what Phase 1 read (D-18),
	// so a concurrent rewrap / re-encrypt / WORM move is never reverted.
	updated := *blob
	updated.StorageRegion = in.TargetRegion
	updated.StorageBucket = targetBucket
	updated.StorageKey = targetKey
	updated.EncryptedDEK = reenc.EncryptedDEK
	updated.DEKNonce = reenc.Nonce
	updated.KEKID = reenc.KEKID
	if err := database.WithTenantTx(ctx, s.cfg.Pool, in.TenantID, func(tx pgx.Tx) error {
		return s.cfg.Repos.ContentBlobs.UpdateMigration(ctx, tx, blob, &updated)
	}); err != nil {
		// Nothing references the new object; the row and the old object
		// are untouched, so the blob reads exactly as before. Drop the
		// new ciphertext so it doesn't leak.
		if derr := s.cfg.S3.DeleteObject(ctx, targetBucket, targetKey); derr != nil {
			s.log.Error().
				Err(derr).
				Str("orphan_bucket", targetBucket).
				Str("orphan_key", targetKey).
				Str("blob_id", blob.ID.String()).
				Msg("reencrypt: db update failed and new ciphertext could not be removed; object orphaned")
		}
		return nil, fmt.Errorf("update blob: %w", err)
	}

	// Phase 8: remove the old ciphertext only after commit (its key always
	// differs from the new one, even within one bucket).
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

	return &ReencryptBlobResult{
		BlobID:         blob.ID,
		SourceRegion:   blob.StorageRegion,
		TargetRegion:   in.TargetRegion,
		SourceBucket:   blob.StorageBucket,
		TargetBucket:   targetBucket,
		TargetKey:      targetKey,
		NewKEKAlias:    reenc.KEKID,
		BytesRewrapped: int64(len(reenc.Ciphertext)),
	}, nil
}

// reencryptSegment marks the generation directory a re-encrypt writes into.
const reencryptSegment = "re-"

// reencryptedKey returns a fresh object key for a re-encrypted blob. It
// keeps the original {tenant}/{yyyy}/{mm}/{upload_id}/ prefix and filename
// (the document service parses the upload id out of segment 4) and adds a
// per-generation directory, replacing — not nesting — the one a previous
// re-encrypt added:
//
//	t/2026/10/u/file.pdf        → t/2026/10/u/re-<gen>/file.pdf
//	t/2026/10/u/re-<old>/file.pdf → t/2026/10/u/re-<gen>/file.pdf
func reencryptedKey(oldKey string, gen uuid.UUID) string {
	dir, file := path.Split(oldKey)
	dir = strings.TrimSuffix(dir, "/")
	if strings.HasPrefix(path.Base(dir), reencryptSegment) {
		dir = path.Dir(dir)
	}
	seg := reencryptSegment + gen.String()
	if dir == "" || dir == "." {
		return seg + "/" + file
	}
	return dir + "/" + seg + "/" + file
}
