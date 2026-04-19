package service

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/database"
	"github.com/vaultdms/vaultdms/pkg/storage"
	"github.com/vaultdms/vaultdms/services/storage/internal/repository"
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

func (r *BlobReaper) reap(ctx context.Context) {
	cutoff := time.Now().Add(-r.grace)
	blobs, err := r.repos.ContentBlobs.ListZeroRefOlderThan(ctx, r.pool, cutoff, r.batch)
	if err != nil {
		r.log.Error().Err(err).Msg("reaper: list zero-ref blobs")
		return
	}
	if len(blobs) == 0 {
		return
	}
	deleted := 0
	for _, b := range blobs {
		if err := r.s3.DeleteObject(ctx, b.StorageBucket, b.StorageKey); err != nil {
			r.log.Warn().Err(err).
				Str("bucket", b.StorageBucket).
				Str("key", b.StorageKey).
				Msg("reaper: s3 delete failed; will retry next cycle")
			continue
		}
		err := database.WithTenantTx(ctx, r.pool, b.TenantID, func(tx pgx.Tx) error {
			return r.repos.ContentBlobs.HardDelete(ctx, tx, b.TenantID, b.ID)
		})
		if err != nil {
			r.log.Warn().Err(err).
				Str("blob_id", b.ID.String()).
				Msg("reaper: db delete failed; S3 object already removed")
			continue
		}
		deleted++
	}
	r.log.Info().Int("scanned", len(blobs)).Int("deleted", deleted).Msg("reaper cycle done")
}
