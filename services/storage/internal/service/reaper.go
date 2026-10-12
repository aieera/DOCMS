package service

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/pkg/storage"
	"github.com/aieera/sedoc/services/storage/internal/model"
	"github.com/aieera/sedoc/services/storage/internal/repository"
)

// BlobReaper periodically sweeps content_blobs rows with reference_count=0
// that have sat unreferenced past the grace window (24h by default),
// deletes the backing S3 object, then hard-deletes the row.
//
// The grace window exists so that a transient unreference (e.g. document
// service deletes a doc, then a retry restores it) does not immediately
// orphan bytes that may still be needed.
type BlobReaper struct {
	pool     *pgxpool.Pool
	repos    *repository.Bundle
	s3       *storage.S3Client
	log      zerolog.Logger
	interval time.Duration
	grace    time.Duration
	batch    int
	stop     chan struct{}
}

// NewBlobReaper returns a reaper with sensible defaults: 1h tick, 24h
// grace, 100-blob batches.
func NewBlobReaper(pool *pgxpool.Pool, repos *repository.Bundle, s3 *storage.S3Client, log zerolog.Logger) *BlobReaper {
	return &BlobReaper{
		pool:     pool,
		repos:    repos,
		s3:       s3,
		log:      log,
		interval: time.Hour,
		grace:    24 * time.Hour,
		batch:    100,
		stop:     make(chan struct{}),
	}
}

// Start runs the reap loop until ctx is cancelled or Stop is called.
// Safe to call in a goroutine.
func (r *BlobReaper) Start(ctx context.Context) {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	r.reap(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stop:
			return
		case <-t.C:
			r.reap(ctx)
		}
	}
}

// Stop signals the loop to exit on its next iteration.
func (r *BlobReaper) Stop() {
	select {
	case <-r.stop:
	default:
		close(r.stop)
	}
}

// reap runs one cycle. It walks the tenant registry itself and reaps each
// tenant's candidates under that tenant's context; a tenant whose listing
// fails is logged and skipped so it cannot stall reclamation for everyone
// else (D-21 — database.ForEachTenant aborted the whole cycle on the first
// error and discarded what it had gathered). At most r.batch blobs are
// examined per cycle.
func (r *BlobReaper) reap(ctx context.Context) {
	tenants, err := database.ListTenantIDs(ctx, r.pool)
	if err != nil {
		r.log.Error().Err(err).Msg("reaper: enumerate tenants")
		return
	}
	cutoff := time.Now().Add(-r.grace)
	scanned, deleted, failed := 0, 0, 0
	for _, tid := range tenants {
		if scanned >= r.batch {
			break
		}
		var blobs []*model.ContentBlob
		err := database.WithTenantTx(ctx, r.pool, tid, func(tx pgx.Tx) error {
			var lerr error
			blobs, lerr = r.repos.ContentBlobs.ListZeroRefOlderThan(ctx, tx, tid, cutoff, r.batch-scanned)
			return lerr
		})
		if err != nil {
			failed++
			r.log.Warn().Err(err).Str("tenant_id", tid.String()).
				Msg("reaper: list zero-ref blobs failed for tenant; skipping it this cycle")
			continue
		}
		scanned += len(blobs)
		deleted += r.reapBlobs(ctx, blobs)
	}
	if scanned == 0 && failed == 0 {
		return
	}
	r.log.Info().Int("scanned", scanned).Int("deleted", deleted).Int("failed_tenants", failed).
		Msg("reaper cycle done")
}

// reapBlobs deletes the given zero-ref blobs (row first, then bytes) and
// returns how many were fully removed.
func (r *BlobReaper) reapBlobs(ctx context.Context, blobs []*model.ContentBlob) int {
	deleted := 0
	for _, b := range blobs {
		// Never reap a WORM-locked blob. Hard-deleting the row destroys the only
		// copy of the wrapped DEK (encrypted_dek/dek_nonce/kek_id) → the retained
		// ciphertext becomes permanently undecryptable, i.e. a crypto-shred BEFORE
		// retention expiry. S3 object-lock protects the bytes but not the DB DEK,
		// so we must fail closed here. (Full fix = DB retain_until/legal_hold state;
		// see docs/security/epic6-storage-followups.md #5.)
		if isWORMBucket(b.StorageBucket, b.StorageRegion) {
			r.log.Warn().Str("blob_id", b.ID.String()).Str("bucket", b.StorageBucket).
				Msg("reaper: skipping WORM-locked blob (retained; not eligible for reap)")
			continue
		}
		// Delete the DB row FIRST, guarded on reference_count=0 (inside HardDelete).
		// If the blob was re-referenced (dedup GetByHash+IncrementRefCount) between
		// the list query and here, HardDelete matches 0 rows (ErrNotFound) and we
		// must NOT touch the backing bytes — deleting them would leave a live row
		// pointing at a destroyed object (silent data loss). Only once the row is
		// provably gone do we best-effort delete the S3 object; a failed S3 delete
		// then merely leaks bytes rather than losing still-referenced data.
		err := database.WithTenantTx(ctx, r.pool, b.TenantID, func(tx pgx.Tx) error {
			return r.repos.ContentBlobs.HardDelete(ctx, tx, b.TenantID, b.ID)
		})
		if err != nil {
			if errors.Is(err, vdmserr.ErrNotFound) {
				// Re-referenced during the grace window; keep the bytes.
				continue
			}
			r.log.Warn().Err(err).
				Str("blob_id", b.ID.String()).
				Msg("reaper: db delete failed; leaving blob for next cycle")
			continue
		}
		if err := r.s3.DeleteObject(ctx, b.StorageBucket, b.StorageKey); err != nil {
			r.log.Warn().Err(err).
				Str("bucket", b.StorageBucket).
				Str("key", b.StorageKey).
				Msg("reaper: s3 delete failed after row removed; object orphaned (storage leak, not data loss)")
			continue
		}
		deleted++
	}
	return deleted
}
