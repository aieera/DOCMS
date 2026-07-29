// WORM (write-once-read-many) object-lock. Admin-triggered per document
// (E... blob-WORM): copy a blob's ciphertext into the region's object-lock
// bucket and apply an S3 retention until a future date. COMPLIANCE mode means
// the object cannot be overwritten or deleted before the date by anyone — the
// S3-enforced half of "a WORM doc cannot be overwritten/deleted before
// retention".
//
// REVIEW-ONLY in this environment: object-lock requires a MinIO/S3 instance
// with object-locking enabled on a versioned bucket; it can't be exercised
// here. The code paths compile against minio-go and mirror reencrypt.go.
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/storage/internal/model"
)

type ApplyWORMInput struct {
	TenantID    uuid.UUID
	BlobID      uuid.UUID
	RetainUntil time.Time
	Mode        string // GOVERNANCE | COMPLIANCE (default COMPLIANCE)
}

type ApplyWORMResult struct {
	BlobID      uuid.UUID `json:"blob_id"`
	WORMBucket  string    `json:"worm_bucket"`
	StorageKey  string    `json:"storage_key"`
	RetainUntil time.Time `json:"retain_until"`
	Mode        string    `json:"mode"`
}

// isWORMBucket reports whether a storage bucket is a region's object-lock
// (WORM) bucket. ApplyWORM repoints a locked blob's storage_bucket here, so this
// is the only in-DB signal of retention until dedicated retain_until/legal_hold
// columns exist (see docs/security/epic6-storage-followups.md, #5). Destructive
// paths (reaper hard-delete, reencrypt-into-plain-bucket) MUST fail closed on a
// WORM bucket to avoid crypto-shredding / unlocking a still-retained blob.
func isWORMBucket(bucket, region string) bool {
	return bucket == bucketName(region, "worm")
}

// ApplyWORM locks a blob into the region's object-lock bucket until retainUntil.
func (s *Service) ApplyWORM(ctx context.Context, in ApplyWORMInput) (*ApplyWORMResult, error) {
	if in.RetainUntil.IsZero() || in.RetainUntil.Before(time.Now()) {
		return nil, vdmserr.Validation("retain_until", "must be a future time")
	}
	mode := in.Mode
	if mode == "" {
		mode = "COMPLIANCE"
	}
	if mode != "GOVERNANCE" && mode != "COMPLIANCE" {
		return nil, vdmserr.Validation("mode", "must be GOVERNANCE or COMPLIANCE")
	}

	var blob *model.ContentBlob
	if err := database.WithTenantTx(ctx, s.cfg.Pool, in.TenantID, func(tx pgx.Tx) error {
		b, err := s.cfg.Repos.ContentBlobs.GetByID(ctx, tx, in.TenantID, in.BlobID)
		if err != nil {
			return err
		}
		blob = b
		return nil
	}); err != nil {
		return nil, err
	}

	wormBucket := bucketName(blob.StorageRegion, "worm")
	// 1. Ensure the object-lock (versioned) bucket exists.
	if err := s.cfg.S3.EnsureObjectLockBucket(ctx, wormBucket, blob.StorageRegion); err != nil {
		return nil, fmt.Errorf("ensure worm bucket: %w", err)
	}
	// 2. Server-side copy the ciphertext into the worm bucket (same key).
	if err := s.cfg.S3.CopyObject(ctx, blob.StorageBucket, blob.StorageKey, wormBucket, blob.StorageKey); err != nil {
		return nil, fmt.Errorf("copy to worm: %w", err)
	}
	// 3. Apply object-lock retention — the S3-enforced WORM guarantee.
	if err := s.cfg.S3.SetObjectRetention(ctx, wormBucket, blob.StorageKey, mode, in.RetainUntil); err != nil {
		return nil, fmt.Errorf("set retention: %w", err)
	}
	// 4. Repoint the blob row at the worm bucket so reads/downloads resolve
	//    to the locked copy.
	updated := *blob
	updated.StorageBucket = wormBucket
	if err := database.WithTenantTx(ctx, s.cfg.Pool, in.TenantID, func(tx pgx.Tx) error {
		return s.cfg.Repos.ContentBlobs.UpdateMigration(ctx, tx, &updated)
	}); err != nil {
		// Locked copy exists but the pointer flip failed — surface so the
		// caller can retry (idempotent: re-copy + re-lock is safe).
		s.log.Error().Err(err).Str("worm_bucket", wormBucket).Str("key", blob.StorageKey).
			Str("blob_id", blob.ID.String()).Msg("worm: db pointer update failed")
		return nil, fmt.Errorf("update blob: %w", err)
	}

	return &ApplyWORMResult{
		BlobID: in.BlobID, WORMBucket: wormBucket, StorageKey: blob.StorageKey,
		RetainUntil: in.RetainUntil, Mode: mode,
	}, nil
}
