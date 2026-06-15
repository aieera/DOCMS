package sync

import (
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aieera/sedoc/integration/internal/sedoc"
	"github.com/aieera/sedoc/integration/internal/store"
)

// Metrics holds the worker's runtime counters for the Sync Dashboard: total
// events processed, a rolling events-per-minute rate, and observed 429s (which
// should stay ~0 thanks to the proactive limiter). Queue depth + DLQ size come
// from the DB and limiter state from the shared limiter — both assembled by
// MetricsHandler. All methods are safe for concurrent use and nil-safe.
type Metrics struct {
	processed atomic.Int64
	http429   atomic.Int64
	window    windowCounter
	now       func() time.Time
}

// NewMetrics constructs a Metrics with a real clock.
func NewMetrics() *Metrics { return &Metrics{now: time.Now} }

// OnProcessed records one successfully processed event.
func (m *Metrics) OnProcessed() {
	if m == nil {
		return
	}
	m.processed.Add(1)
	m.window.add(m.now().Unix())
}

// On429 records one observed SeDoc 429 response.
func (m *Metrics) On429() {
	if m == nil {
		return
	}
	m.http429.Add(1)
}

// windowCounter is a 60×1-second ring summing events in the trailing minute.
type windowCounter struct {
	mu      sync.Mutex
	buckets [60]int64
	lastSec int64
}

func (w *windowCounter) add(sec int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.advance(sec)
	w.buckets[sec%60]++
}

func (w *windowCounter) rate(sec int64) int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.advance(sec)
	var sum int64
	for _, b := range w.buckets {
		sum += b
	}
	return sum
}

// advance zeroes the buckets for each second elapsed since the last touch, so
// entries older than the 60s window are evicted. Caller holds w.mu.
func (w *windowCounter) advance(sec int64) {
	if w.lastSec == 0 {
		w.lastSec = sec
		return
	}
	gap := sec - w.lastSec
	if gap <= 0 {
		return
	}
	if gap > 60 {
		gap = 60
	}
	for i := int64(1); i <= gap; i++ {
		w.buckets[(w.lastSec+i)%60] = 0
	}
	w.lastSec = sec
}

// MetricsHandler serves the combined operational metrics JSON: worker runtime
// counters + DB-derived backlog/DLQ + shared-limiter state.
func MetricsHandler(m *Metrics, lim *sedoc.Limiter, st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		backlog, dlq, err := st.SyncCounts(r.Context())
		out := map[string]any{
			"events_per_min":  m.window.rate(m.now().Unix()),
			"processed_total": m.processed.Load(),
			"http_429":        m.http429.Load(),
			"backlog":         backlog,
			"dlq":             dlq,
		}
		if err != nil {
			out["error"] = err.Error()
		}
		if lim != nil {
			s := lim.Stats()
			out["limiter"] = map[string]any{
				"limit_per_sec":    s.LimitPerSec,
				"burst":            s.Burst,
				"tokens_available": s.TokensAvailable,
				"wait_count":       s.WaitCount,
				"wait_total_ms":    s.WaitTotal.Milliseconds(),
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}
