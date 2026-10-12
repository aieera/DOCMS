package service

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/database"
)

// ExpirySweepInterval is how often RunExpirySweeper releases abandoned
// upload sessions.
const ExpirySweepInterval = 5 * time.Minute

// SweepExpiredUploads marks every in-flight upload session (initiated /
// uploading / scanning) past its expires_at as failed and returns how many
// it touched. It enumerates tenants from the organizations registry and
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
			n, merr = s.repos.Uploads.MarkExpired(ctx, tx, tid, before)
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
