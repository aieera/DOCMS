package service

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/services/storage/internal/model"
)

// ExpirySweepInterval is how often RunExpirySweeper releases abandoned
// upload sessions.
const ExpirySweepInterval = 5 * time.Minute

// ScanningGrace is how long past expires_at a `scanning` session is still
// treated as owned by its CompleteUpload pipeline. A claim taken just
// before expiry keeps running (fetch, scan, encrypt, re-PUT, persist);
// failing the session underneath it — on a client retry or in the sweep —
// would orphan the pipeline's encrypted overwrite. Comfortably above the
// 2-minute ClamAV timeout plus the S3 round trips.
const ScanningGrace = 15 * time.Minute

// sessionExpired reports whether a session is past its validity window at
// `now`: expires_at for every state, plus ScanningGrace while scanning.
func sessionExpired(u *model.UploadSession, now time.Time) bool {
	limit := u.ExpiresAt
	if u.Status == model.UploadScanning {
		limit = limit.Add(ScanningGrace)
	}
	return now.After(limit)
}

// SweepExpiredUploads marks every in-flight upload session past its
// validity window as failed — initiated/uploading past expires_at,
// scanning past expires_at + ScanningGrace — and returns how many it
// touched. It enumerates tenants from the organizations registry and
// sweeps each under its own tenant context — upload_sessions is FORCE-RLS,
// so one cross-tenant UPDATE matches nothing under the NOBYPASSRLS app role
// (L-6). A failing tenant is logged and skipped so it cannot starve the
// rest (ForEachTenant would abort the whole pass); the returned error
// reports how many tenants failed.
func (s *Service) SweepExpiredUploads(ctx context.Context) (int64, error) {
	tenants, err := database.ListTenantIDs(ctx, s.pool)
	if err != nil {
		return 0, fmt.Errorf("upload expiry sweep: %w", err)
	}
	before := s.now().UTC()
	var total int64
	failed := 0
	for _, tid := range tenants {
		var n int64
		err := database.WithTenantTx(ctx, s.pool, tid, func(tx pgx.Tx) error {
			var merr error
			n, merr = s.repos.Uploads.MarkExpired(ctx, tx, tid, before, before.Add(-ScanningGrace))
			return merr
		})
		if err != nil {
			failed++
			s.log.Warn().Err(err).Str("tenant_id", tid.String()).
				Msg("upload expiry sweep: tenant failed; continuing with the rest")
			continue
		}
		total += n
	}
	if failed > 0 {
		return total, fmt.Errorf("upload expiry sweep: %d of %d tenants failed", failed, len(tenants))
	}
	return total, nil
}

// RunExpirySweeper sweeps once immediately and then every interval until
// ctx is cancelled. Safe to run in a goroutine.
func (s *Service) RunExpirySweeper(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		n, err := s.SweepExpiredUploads(ctx)
		if err != nil {
			s.log.Error().Err(err).Int64("expired", n).Msg("upload expiry sweep")
		} else if n > 0 {
			s.log.Info().Int64("expired", n).Msg("upload expiry sweep: released expired sessions")
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
