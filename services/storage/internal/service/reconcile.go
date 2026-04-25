package service

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/database"
	"github.com/vaultdms/vaultdms/services/storage/internal/model"
	"github.com/vaultdms/vaultdms/services/storage/internal/repository"
)

// ScanReconciler sweeps scan_results rows left in 'pending' past the grace
// window (1h by default) and re-enqueues them for rescan. This closes the
// gap where a crash between enqueue and finalize would strand an upload
// with no visible scan outcome — the user would otherwise see a forever
// "scanning" upload.
//
// The reconciler uses a BYPASSRLS connection (same as BlobReaper) because
// the pending set fans across tenants. Re-enqueue happens via the outbox
// under the subject dms.storage.scan_reconcile.v1 — a dedicated worker
// (future wave) consumes it and re-runs the scan against the hot bucket
// object. Until that worker ships, this loop flips the row back to
// scan_result=error so the upload is surfaced in the admin panel and can
// be manually re-uploaded.
type ScanReconciler struct {
	pool     *pgxpool.Pool
	repos    *repository.Bundle
	log      zerolog.Logger
	interval time.Duration
	grace    time.Duration
	batch    int
	stop     chan struct{}
}

// NewScanReconciler returns a reconciler with sensible defaults: 15m tick,
// 1h pending grace, 50-row batches.
func NewScanReconciler(pool *pgxpool.Pool, repos *repository.Bundle, log zerolog.Logger) *ScanReconciler {
	return &ScanReconciler{
		pool:     pool,
		repos:    repos,
		log:      log,
		interval: 15 * time.Minute,
		grace:    1 * time.Hour,
		batch:    50,
		stop:     make(chan struct{}),
	}
}

// Start runs the sweep loop until ctx is cancelled or Stop is called.
func (r *ScanReconciler) Start(ctx context.Context) {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	r.sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stop:
			return
		case <-t.C:
			r.sweep(ctx)
		}
	}
}

// Stop signals the loop to exit on its next iteration.
func (r *ScanReconciler) Stop() {
	select {
	case <-r.stop:
	default:
		close(r.stop)
	}
}

func (r *ScanReconciler) sweep(ctx context.Context) {
	cutoff := time.Now().Add(-r.grace)
	stuck, err := r.repos.Scans.ListStuckPending(ctx, r.pool, cutoff, r.batch)
	if err != nil {
		r.log.Error().Err(err).Msg("reconciler: list pending")
		return
	}
	if len(stuck) == 0 {
		return
	}
	for _, rec := range stuck {
		err := database.WithTenantTx(ctx, r.pool, rec.TenantID, func(tx pgx.Tx) error {
			// Flip to error so operators see it. A dedicated re-scan
			// worker (future) will consume the re-enqueue event and
			// either restore clean status or escalate.
			_, err := tx.Exec(ctx, `
				UPDATE scan_results
				SET result = $1, scanned_at = now()
				WHERE tenant_id = $2 AND id = $3 AND result = 'pending'
			`, string(model.ScanError), rec.TenantID, rec.ID)
			return err
		})
		if err != nil {
			r.log.Warn().Err(err).Str("scan_id", rec.ID.String()).Msg("reconciler: flip failed")
			continue
		}
		reconcilePendingSweep.Inc()
	}
	r.log.Info().Int("swept", len(stuck)).Msg("scan reconcile cycle done")
}
