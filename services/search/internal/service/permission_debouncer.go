// ADR 0066 §"Permission-change propagation" — debounced batch
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
// match ADR 0066 §"SLI": [0.1, 0.5, 1, 2, 5, 10, 30] — the 5s mark
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
			Help:    "Time from dms.permission.changed.v1 emission to OpenSearch index update commit. ADR 0066.",
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
		// UpdateReadableByFolder takes a single readable_by slice;
		// pull it out of fields. Pre-split fields stay in the
		// current implementation as a follow-up.
		rb, _ := p.fields["readable_by"].([]string)
		err = d.svc.UpdateReadableByFolder(ctx, p.resource.tenantID, p.resource.resourceID, rb)
	case "workspace":
		rb, _ := p.fields["readable_by"].([]string)
		err = d.svc.UpdateReadableByWorkspace(ctx, p.resource.tenantID, p.resource.resourceID, rb)
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
