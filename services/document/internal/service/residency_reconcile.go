package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/rs/zerolog"
)

// ResidencyReconciler computes the data_residency_compliance SLI on a
// daily tick. The SLI definition (Blueprint §9.1):
//
//	data_residency_compliance = docs_in_correct_region / total_docs
//
// "Correct region" = the document's content_blob.storage_region equals
// the document's region_pin. Anything else is an off-region doc and
// counts against the SLI. The worker pushes:
//
//   - data_residency_compliance{tenant} — gauge in [0, 1].
//   - residency_off_region_docs{tenant,region} — gauge of stranded docs
//     per region, scoped to one tenant per series.
//
// Cross-tenant aggregation happens in Prometheus, not here — this
// worker stays tenant-scoped to keep the per-tenant cardinality
// stable.
type ResidencyReconciler struct {
	pool     *pgxpool.Pool
	log      zerolog.Logger
	interval time.Duration
	stop     chan struct{}
}

var (
	residencyComplianceGauge = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "data_residency_compliance",
			Help: "Per-tenant residency SLI in [0,1]: docs in correct region / total docs.",
		},
		[]string{"tenant"},
	)
	residencyOffRegionGauge = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "residency_off_region_docs",
			Help: "Documents whose current content_blob storage_region differs from their region_pin.",
		},
		[]string{"tenant", "region"},
	)
)

// NewResidencyReconciler returns a worker with sensible defaults: 24h
// tick, scoped to the document service's pool.
func NewResidencyReconciler(pool *pgxpool.Pool, log zerolog.Logger) *ResidencyReconciler {
	return &ResidencyReconciler{
		pool:     pool,
		log:      log,
		interval: 24 * time.Hour,
		stop:     make(chan struct{}),
	}
}

// Start runs the sweep loop until ctx is cancelled or Stop is called.
// First sweep fires immediately so the gauge is populated before the
// first scrape window — without this, a fresh deployment shows "no
// data" on the dashboard for up to 24h.
func (r *ResidencyReconciler) Start(ctx context.Context) {
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
func (r *ResidencyReconciler) Stop() {
	select {
	case <-r.stop:
	default:
		close(r.stop)
	}
}

// Sweep runs one cycle synchronously. Used by tests + admin "run now"
// button (future) without forcing a full Start/Stop dance.
func (r *ResidencyReconciler) Sweep(ctx context.Context) { r.sweep(ctx) }

// sweep iterates every tenant, computes per-tenant compliance + per-
// region off counts, publishes Prom gauges. Uses a non-tenant-scoped
// query (the worker is platform-side, not RLS-scoped) to avoid
// fanning out N queries.
func (r *ResidencyReconciler) sweep(ctx context.Context) {
	rows, err := r.pool.Query(ctx, `
		SELECT
		    d.tenant_id,
		    d.region_pin,
		    COUNT(*) FILTER (WHERE cb.storage_region IS NOT NULL AND cb.storage_region <> d.region_pin) AS off_count,
		    COUNT(*) AS total
		  FROM documents d
		  LEFT JOIN versions v
		    ON v.tenant_id = d.tenant_id
		   AND v.id = d.current_version_id
		  LEFT JOIN content_blobs cb
		    ON cb.tenant_id = d.tenant_id
		   AND cb.id = v.content_blob_id
		 WHERE d.deleted_at IS NULL
		 GROUP BY d.tenant_id, d.region_pin
	`)
	if err != nil {
		r.log.Error().Err(err).Msg("residency reconcile: query failed")
		return
	}
	defer rows.Close()

	type acc struct{ off, total int64 }
	tenantTotals := make(map[uuid.UUID]acc)
	for rows.Next() {
		var (
			tenantID uuid.UUID
			region   string
			offCount int64
			total    int64
		)
		if err := rows.Scan(&tenantID, &region, &offCount, &total); err != nil {
			r.log.Warn().Err(err).Msg("residency reconcile: scan failed")
			continue
		}
		residencyOffRegionGauge.WithLabelValues(tenantID.String(), region).Set(float64(offCount))
		t := tenantTotals[tenantID]
		t.off += offCount
		t.total += total
		tenantTotals[tenantID] = t
	}
	for tenantID, a := range tenantTotals {
		var compliance float64 = 1.0
		if a.total > 0 {
			compliance = float64(a.total-a.off) / float64(a.total)
		}
		residencyComplianceGauge.WithLabelValues(tenantID.String()).Set(compliance)
	}
	r.log.Info().Int("tenants", len(tenantTotals)).Msg("residency reconcile cycle done")
}
