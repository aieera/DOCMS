// Package janitor holds background sweepers owned by the document
// service. Each sweeper is a long-running goroutine started from
// main.go that periodically reconciles drift the request path can
// leave behind.
package janitor

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/database"
)

// OrphanGC sweeps documents rows whose upload never completed.
//
// The upload flow is: web client POSTs to create a documents row,
// then asks the storage service for an initiate-upload session, PUTs
// the bytes to S3, and finally calls complete-upload — the document
// service receives a NATS event and writes the versions row + sets
// documents.current_version_id. If the client crashes (closed tab,
// browser kill, lost connection) between create and complete, the
// documents row sits forever with current_version_id IS NULL.
//
// These rows are filtered out client-side, but they still consume
// rows + index pages in every tenant. The sweeper soft-deletes
// orphans older than `Grace` so the row count stays bounded.
//
// We intentionally only soft-delete (deleted_at = now()). Hard delete
// is owned by the existing retention pipeline so cascading cleanup
// (audit refs, etc.) goes through one code path.
type OrphanGC struct {
	pool     *pgxpool.Pool
	log      zerolog.Logger
	interval time.Duration
	grace    time.Duration
	batch    int
	stop     chan struct{}
}

// New returns a sweeper with sensible defaults: tick every hour,
// 24h grace, soft-delete in batches of 200. Defaults are tuned for
// healthy workloads — the orphan set should normally be ≤ a handful
// per tenant per day.
func New(pool *pgxpool.Pool, log zerolog.Logger) *OrphanGC {
	return &OrphanGC{
		pool:     pool,
		log:      log.With().Str("component", "orphan_gc").Logger(),
		interval: time.Hour,
		grace:    24 * time.Hour,
		batch:    200,
		stop:     make(chan struct{}),
	}
}

// Start runs the sweep loop until ctx is cancelled or Stop is
// called. Safe to call in a goroutine; the first sweep runs
// immediately so a fresh deploy reconciles existing drift without
// waiting a tick.
func (g *OrphanGC) Start(ctx context.Context) {
	t := time.NewTicker(g.interval)
	defer t.Stop()
	g.sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-g.stop:
			return
		case <-t.C:
			g.sweep(ctx)
		}
	}
}

// Stop signals the loop to exit on its next iteration. Safe to
// call multiple times — second Stop is a no-op.
func (g *OrphanGC) Stop() {
	select {
	case <-g.stop:
	default:
		close(g.stop)
	}
}

// orphan is a minimal row shape — (tenant_id, id) is enough to
// drive the per-tenant soft-delete loop.
type orphan struct {
	TenantID uuid.UUID
	ID       uuid.UUID
}

// sweep does one cycle: list candidates across all tenants, then
// soft-delete each under its own tenant TX. Errors per-row are
// logged and skipped so one bad row doesn't stall the loop.
func (g *OrphanGC) sweep(ctx context.Context) {
	cutoff := time.Now().UTC().Add(-g.grace)
	orphans, err := g.listOrphans(ctx, cutoff, g.batch)
	if err != nil {
		g.log.Error().Err(err).Msg("list orphans")
		return
	}
	if len(orphans) == 0 {
		return
	}
	reaped := 0
	for _, o := range orphans {
		if err := g.softDelete(ctx, o); err != nil {
			g.log.Warn().Err(err).
				Str("tenant_id", o.TenantID.String()).
				Str("document_id", o.ID.String()).
				Msg("orphan soft-delete failed")
			continue
		}
		reaped++
	}
	g.log.Info().
		Int("scanned", len(orphans)).
		Int("reaped", reaped).
		Time("cutoff", cutoff).
		Msg("orphan gc cycle done")
}

// listOrphans returns up to `limit` orphan rows. Every documents row
// carries a tenant_id, so this is per-tenant work: enumerate tenants
// from the organizations registry and read each tenant's orphans under
// that tenant's app.current_tenant (Wave A.1, issue #76 — same fix as
// the storage BlobReaper). The former `SET LOCAL row_security = off`
// ERRORED under the prod NOBYPASSRLS role, killing the sweep every
// cycle. No bypass now.
func (g *OrphanGC) listOrphans(ctx context.Context, cutoff time.Time, limit int) ([]orphan, error) {
	out := make([]orphan, 0, limit)
	err := database.ForEachTenant(ctx, g.pool, func(tenantID uuid.UUID, tx pgx.Tx) error {
		if len(out) >= limit {
			return nil // batch already full; remaining tenants are no-ops
		}
		rows, err := tx.Query(ctx, `
			SELECT tenant_id, id
			FROM documents
			WHERE tenant_id = $1
			  AND current_version_id IS NULL
			  AND deleted_at IS NULL
			  AND created_at < $2
			ORDER BY created_at ASC
			LIMIT $3`, tenantID, cutoff, limit-len(out))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o orphan
			if err := rows.Scan(&o.TenantID, &o.ID); err != nil {
				return err
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// softDelete sets deleted_at under the tenant's RLS context. The
// extra guards (current_version_id IS NULL, deleted_at IS NULL,
// created_at < cutoff) re-check the candidate at write time so a
// race where the upload completes between list and delete leaves
// the now-real document untouched.
func (g *OrphanGC) softDelete(ctx context.Context, o orphan) error {
	conn, err := g.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", o.TenantID.String()); err != nil {
		return err
	}
	cutoff := time.Now().UTC().Add(-g.grace)
	if _, err := tx.Exec(ctx, `
		UPDATE documents
		   SET deleted_at = now(),
		       updated_at = now()
		 WHERE tenant_id = $1
		   AND id = $2
		   AND current_version_id IS NULL
		   AND deleted_at IS NULL
		   AND created_at < $3`,
		o.TenantID, o.ID, cutoff); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
