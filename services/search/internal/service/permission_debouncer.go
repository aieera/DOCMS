// ADR 0083 §"Permission-change propagation" — debounced batch
// reindex on permission events.
//
// Without debouncing, a bulk grant rollout (e.g. an admin adding a
// group to a workspace with 100k docs) fans out to 100k separate
// OpenSearch update round-trips inside a few seconds. The
// debouncer coalesces multiple events for the same
// (tenant, resource_type, resource_id) into one update, flushed
// 5s after the last tickle.
//
// Trade-off: a single grant change takes up to 5s to land vs.
// sub-second today. The §7.3 SLI metric makes the regression
// visible; the bulk-grant scenario is what we're optimizing for.
package service

import (
	"context"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	dto "github.com/prometheus/client_model/go"
	"github.com/rs/zerolog"
)

// debounceWindow is the §7.3 SLI ceiling. Drives both the per-key
// deadline and the flusher tick rate.
const debounceWindow = 5 * time.Second

// flushInterval — how often the flusher loop wakes to walk the map.
// 1s gives sub-debounceWindow tail latency on the typical case
// (one event, no further tickles) without burning CPU on an idle
// service.
const flushInterval = 1 * time.Second

// pendingUpdate captures the most-recent state for a key. The
// debouncer always overwrites — the latest event wins, since
// readable_by is a full-set replacement, not a delta.
type pendingUpdate struct {
	deadline time.Time
	// eventTime — when the upstream emitted the change. Used to
	// observe the propagation-lag SLI at flush time, NOT to decide
	// when to flush (deadline is the wall-clock-from-now value).
	eventTime time.Time
	// fields is the OpenSearch partial-update body. Same shape the
	// non-debounced PartialUpdate accepts, so the flusher just
	// hands it off.
	fields map[string]any
	// resource carries enough context to route the flush to the
	// right service method.
	resource debounceKey
}

// debounceKey identifies a coalescing bucket. (tenant, type, id) is
// the natural granularity — multiple events for the same document
// collapse, but events for different documents stay independent.
type debounceKey struct {
	tenantID     string
	resourceType string
	resourceID   string
}

// PermissionDebouncer coalesces dms.permission.changed.v1 events.
// Construct with NewPermissionDebouncer; call Submit per event;
// stop with Close on shutdown so the flusher drains.
type PermissionDebouncer struct {
	svc *Service
	log zerolog.Logger

	mu      sync.Mutex
	pending map[debounceKey]pendingUpdate

	// flushHook fires after every flush attempt so tests can observe
	// without timing-races. Nil in production.
	flushHook func(key debounceKey, lag time.Duration, err error)

	// observer is the lag-metric callback wired by the cmd/server
	// startup so this package doesn't import prometheus directly
	// (keeps test imports light).
	observer LagObserver

	stop   chan struct{}
	doneWg sync.WaitGroup
}

// LagObserver receives the per-flush propagation lag. Implementations
// typically push to a Prometheus histogram + a counter for
// success/failure.
type LagObserver interface {
	ObservePropagation(lag time.Duration, success bool)
}

// promLagObserver is the production observer — wires
// search_permission_propagation_lag_seconds (histogram) and
// search_permission_propagation_total{result} (counter). Buckets
// match ADR 0083 §"SLI": [0.1, 0.5, 1, 2, 5, 10, 30] — the 5s mark
// is the alert threshold the runbook will eventually wire as
// `histogram_quantile(0.95, …) > 5`.
type promLagObserver struct {
	histogram *prometheus.HistogramVec
	counter   *prometheus.CounterVec
}

var (
	permissionPropagationLag = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "search_permission_propagation_lag_seconds",
			Help:    "Time from dms.permission.changed.v1 emission to OpenSearch index update commit. ADR 0083.",
			Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30},
		},
		[]string{"result"},
	)
	permissionPropagationTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "search_permission_propagation_total",
			Help: "Count of permission propagations by outcome (success|failure).",
		},
		[]string{"result"},
	)
)

func (o *promLagObserver) ObservePropagation(lag time.Duration, success bool) {
	result := "success"
	if !success {
		result = "failure"
	}
	o.histogram.WithLabelValues(result).Observe(lag.Seconds())
	o.counter.WithLabelValues(result).Inc()
}

// NewLagObserver returns the production observer. Wired from
// cmd/server during startup so the histogram + counter register
// against the default Prometheus registry — visible at /metrics
// without per-service plumbing.
func NewLagObserver() LagObserver {
	return &promLagObserver{
		histogram: permissionPropagationLag,
		counter:   permissionPropagationTotal,
	}
}

// NewPermissionDebouncer wires the debouncer to a search Service.
// The flusher loop starts when Run is called — usually from main()
// during service startup.
func NewPermissionDebouncer(svc *Service, log zerolog.Logger, observer LagObserver) *PermissionDebouncer {
	return &PermissionDebouncer{
		svc:      svc,
		log:      log,
		pending:  make(map[debounceKey]pendingUpdate),
		observer: observer,
		stop:     make(chan struct{}),
	}
}

// Submit records (or updates) a pending propagation. Always
// overwrites the existing entry's fields — the latest snapshot is
// the one we want to land.
func (d *PermissionDebouncer) Submit(
	tenantID, resourceType, resourceID string,
	fields map[string]any,
	eventTime time.Time,
) {
	if eventTime.IsZero() {
		eventTime = time.Now()
	}
	key := debounceKey{tenantID, resourceType, resourceID}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pending[key] = pendingUpdate{
		deadline:  time.Now().Add(debounceWindow),
		eventTime: eventTime,
		fields:    fields,
		resource:  key,
	}
}

// Run starts the flusher loop. Returns when ctx is cancelled OR
// Close is called.
func (d *PermissionDebouncer) Run(ctx context.Context) {
	d.doneWg.Add(1)
	defer d.doneWg.Done()
	t := time.NewTicker(flushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			d.flushAll(context.Background())
			return
		case <-d.stop:
			d.flushAll(context.Background())
			return
		case now := <-t.C:
			d.flushDue(ctx, now)
		}
	}
}

// Close signals Run to exit; blocks until the loop returns.
func (d *PermissionDebouncer) Close() {
	close(d.stop)
	d.doneWg.Wait()
}

// flushDue walks the map and flushes every entry whose deadline has
// passed. Entries that haven't reached their deadline stay queued —
// further Submits push the deadline back.
func (d *PermissionDebouncer) flushDue(ctx context.Context, now time.Time) {
	d.mu.Lock()
	due := make([]pendingUpdate, 0)
	for k, p := range d.pending {
		if !p.deadline.After(now) {
			due = append(due, p)
			delete(d.pending, k)
		}
	}
	d.mu.Unlock()
	for _, p := range due {
		d.flushOne(ctx, p)
	}
}

// flushAll drains every queued entry — called on shutdown so we
// don't lose in-flight propagations to a SIGTERM.
func (d *PermissionDebouncer) flushAll(ctx context.Context) {
	d.mu.Lock()
	due := make([]pendingUpdate, 0, len(d.pending))
	for _, p := range d.pending {
		due = append(due, p)
	}
	d.pending = make(map[debounceKey]pendingUpdate)
	d.mu.Unlock()
	for _, p := range due {
		d.flushOne(ctx, p)
	}
}

func (d *PermissionDebouncer) flushOne(ctx context.Context, p pendingUpdate) {
	var err error
	switch p.resource.resourceType {
	case "document":
		err = d.svc.PartialUpdate(ctx, p.resource.tenantID, p.resource.resourceID, p.fields)
	case "folder":
		// Pass the full coalesced fields map so the split readable_by_users/
		// _groups are updated alongside readable_by (Epic 9 #2 — updating only
		// the mixed field left the split fields the query matches on stale).
		err = d.svc.UpdateReadableByFolder(ctx, p.resource.tenantID, p.resource.resourceID, p.fields)
	case "workspace":
		err = d.svc.UpdateReadableByWorkspace(ctx, p.resource.tenantID, p.resource.resourceID, p.fields)
	default:
		// Already filtered upstream by indexer.go::onPermissionChanged;
		// belt + suspenders here so a misuse can't silently no-op.
		d.log.Warn().Str("type", p.resource.resourceType).
			Msg("debouncer: unknown resource_type")
		return
	}
	lag := time.Since(p.eventTime)
	if d.observer != nil {
		d.observer.ObservePropagation(lag, err == nil)
	}
	if d.flushHook != nil {
		d.flushHook(p.resource, lag, err)
	}
	if err != nil {
		d.log.Error().Err(err).
			Str("resource", p.resource.resourceType+"/"+p.resource.resourceID).
			Dur("lag", lag).
			Msg("debounced propagation failed")
	}
}

// PendingCount returns the queue depth — used by tests + an
// admin-dashboard health check.
func (d *PermissionDebouncer) PendingCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.pending)
}

// PropagationStats is the JSON shape the admin endpoint serves.
// Pulled from the live Prometheus histogram so /metrics scraping
// isn't a prerequisite — the admin page works on a fresh deploy
// before Grafana is wired.
type PropagationStats struct {
	P50Seconds   float64 `json:"p50_seconds"`
	P95Seconds   float64 `json:"p95_seconds"`
	P99Seconds   float64 `json:"p99_seconds"`
	TotalSuccess uint64  `json:"total_success"`
	TotalFailure uint64  `json:"total_failure"`
	PendingCount int     `json:"pending_count"`
}

// CollectPropagationStats reads the in-process Prometheus histogram
// and returns the SLI summary for the admin dashboard. Approximate
// quantiles are computed from cumulative bucket counts using the
// usual linear-interpolation formula — matches the
// histogram_quantile() PromQL function the runbook will alert on.
func (d *PermissionDebouncer) CollectPropagationStats() PropagationStats {
	stats := PropagationStats{PendingCount: d.PendingCount()}
	successHist, _ := readHistogram(permissionPropagationLag, "success")
	if successHist != nil {
		stats.P50Seconds = histogramQuantile(successHist, 0.50)
		stats.P95Seconds = histogramQuantile(successHist, 0.95)
		stats.P99Seconds = histogramQuantile(successHist, 0.99)
	}
	stats.TotalSuccess = readCounter(permissionPropagationTotal, "success")
	stats.TotalFailure = readCounter(permissionPropagationTotal, "failure")
	return stats
}

// readHistogram pulls the per-bucket cumulative counts for a label
// value out of a Prometheus HistogramVec. Returns (buckets, count)
// where buckets is a slice of (upperBound, cumulativeCount) pairs
// and count is the total observation count. nil on missing label.
func readHistogram(h *prometheus.HistogramVec, label string) ([]histogramBucket, uint64) {
	m, err := h.GetMetricWithLabelValues(label)
	if err != nil {
		return nil, 0
	}
	pb := &dto.Metric{}
	if err := m.(prometheus.Metric).Write(pb); err != nil {
		return nil, 0
	}
	hist := pb.Histogram
	if hist == nil {
		return nil, 0
	}
	buckets := make([]histogramBucket, 0, len(hist.Bucket))
	for _, b := range hist.Bucket {
		if b.UpperBound != nil && b.CumulativeCount != nil {
			buckets = append(buckets, histogramBucket{
				upperBound: *b.UpperBound,
				count:      *b.CumulativeCount,
			})
		}
	}
	var total uint64
	if hist.SampleCount != nil {
		total = *hist.SampleCount
	}
	return buckets, total
}

// readCounter reads the current value of a Prometheus CounterVec
// at the given label. Returns 0 on missing label rather than
// raising — admin dashboards must handle no-data gracefully.
func readCounter(c *prometheus.CounterVec, label string) uint64 {
	m, err := c.GetMetricWithLabelValues(label)
	if err != nil {
		return 0
	}
	pb := &dto.Metric{}
	if err := m.(prometheus.Metric).Write(pb); err != nil {
		return 0
	}
	cv := pb.Counter
	if cv == nil || cv.Value == nil {
		return 0
	}
	return uint64(*cv.Value)
}

type histogramBucket struct {
	upperBound float64
	count      uint64
}

// histogramQuantile mirrors PromQL's histogram_quantile — takes the
// rank (0-1) and the cumulative-count buckets, returns the
// linearly-interpolated value at that rank. Returns 0 on insufficient
// data.
func histogramQuantile(buckets []histogramBucket, q float64) float64 {
	if len(buckets) == 0 {
		return 0
	}
	total := buckets[len(buckets)-1].count
	if total == 0 {
		return 0
	}
	target := float64(total) * q
	var prevBound float64
	var prevCount uint64
	for _, b := range buckets {
		if float64(b.count) >= target {
			if b.count == prevCount {
				return prevBound
			}
			frac := (target - float64(prevCount)) / float64(b.count-prevCount)
			return prevBound + frac*(b.upperBound-prevBound)
		}
		prevBound = b.upperBound
		prevCount = b.count
	}
	return buckets[len(buckets)-1].upperBound
}
