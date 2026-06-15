package sync

import (
	"testing"
	"time"
)

// TestWindowCounterRollsOverMinute: events drop out of the events/min figure
// once they're older than 60 seconds.
func TestWindowCounterRollsOverMinute(t *testing.T) {
	var w windowCounter
	for i := 0; i < 5; i++ {
		w.add(1000)
	}
	if got := w.rate(1000); got != 5 {
		t.Fatalf("rate at t=1000 = %d, want 5", got)
	}
	for i := 0; i < 3; i++ {
		w.add(1030)
	}
	if got := w.rate(1030); got != 8 {
		t.Fatalf("rate at t=1030 = %d, want 8 (both batches within 60s)", got)
	}
	// At t=1061 the t=1000 batch is 61s old (evicted); the t=1030 batch (31s) stays.
	if got := w.rate(1061); got != 3 {
		t.Fatalf("rate at t=1061 = %d, want 3 (older batch rolled off)", got)
	}
	// Long idle gap clears the whole window.
	if got := w.rate(5000); got != 0 {
		t.Fatalf("rate after long idle = %d, want 0", got)
	}
}

// TestMetricsCounters: processed + 429 counters and the rolling rate; nil-safe.
func TestMetricsCounters(t *testing.T) {
	m := NewMetrics()
	sec := int64(5000)
	m.now = func() time.Time { return time.Unix(sec, 0) }

	m.OnProcessed()
	m.OnProcessed()
	m.On429()

	if got := m.processed.Load(); got != 2 {
		t.Fatalf("processed = %d, want 2", got)
	}
	if got := m.http429.Load(); got != 1 {
		t.Fatalf("http429 = %d, want 1", got)
	}
	if got := m.window.rate(sec); got != 2 {
		t.Fatalf("events/min = %d, want 2", got)
	}

	// nil receiver must not panic (metrics disabled).
	var n *Metrics
	n.OnProcessed()
	n.On429()
}
