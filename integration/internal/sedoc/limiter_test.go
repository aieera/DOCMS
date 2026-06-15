package sedoc

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock drives the limiter without real wall-clock delays: now() reads a
// virtual time and sleep() advances it. This lets a synthetic burst of writes be
// asserted deterministically and instantly.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Unix(0, 0)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
	return nil
}

// recordRT is a stub transport that timestamps (on the fake clock) every request
// the client sends and returns a canned 200, so the test sees exactly when each
// write would leave the process under throttling.
type recordRT struct {
	clock *fakeClock
	body  string
	mu    sync.Mutex
	at    []time.Time
	paths []string
}

func (rt *recordRT) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.at = append(rt.at, rt.clock.now())
	rt.paths = append(rt.paths, r.URL.Path)
	rt.mu.Unlock()
	body := rt.body
	if body == "" {
		body = "{}"
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

func (rt *recordRT) times() []time.Time {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	out := make([]time.Time, len(rt.at))
	copy(out, rt.at)
	return out
}

// limiterOnClock builds a limiter at perSec/burst driven by fc.
func limiterOnClock(fc *fakeClock, perSec float64, burst int) *Limiter {
	l := NewLimiter(perSec, burst)
	l.now = fc.now
	l.sleep = fc.sleep
	return l
}

// maxInWindow returns the largest number of timestamps falling in any half-open
// 1-second window [t, t+1s).
func maxInWindow(ts []time.Time, window time.Duration) int {
	max := 0
	for _, start := range ts {
		end := start.Add(window)
		n := 0
		for _, t := range ts {
			if !t.Before(start) && t.Before(end) {
				n++
			}
		}
		if n > max {
			max = n
		}
	}
	return max
}

// TestLimiterPacesOutboundUnder10PerSec fires a synthetic burst through a client
// whose transport is stubbed and whose limiter runs on a fake clock; it asserts
// the outbound stream never exceeds ~10/s and that pacing actually engaged.
func TestLimiterPacesOutboundUnder10PerSec(t *testing.T) {
	fc := newFakeClock()
	rt := &recordRT{clock: fc}
	c := New("http://sedoc.test/api/v1", "vdms_test").WithLimiter(limiterOnClock(fc, 10, 1))
	c.hc = &http.Client{Transport: rt}

	const n = 25
	for i := 0; i < n; i++ {
		if err := c.doJSON(context.Background(), http.MethodPost, "/ingest", "", nil, nil); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	ts := rt.times()
	if len(ts) != n {
		t.Fatalf("recorded %d requests, want %d", len(ts), n)
	}

	// (a) No 1-second window holds more than burst(1)+10 writes.
	if got := maxInWindow(ts, time.Second); got > 11 {
		t.Fatalf("max writes in any 1s window = %d, want <= 11 (~10/s)", got)
	}

	// (b) Steady-state spacing is >= ~100ms (after the single-token burst).
	for i := 2; i < len(ts); i++ {
		if gap := ts[i].Sub(ts[i-1]); gap < 99*time.Millisecond {
			t.Fatalf("gap between write %d and %d = %v, want >= ~100ms", i-1, i, gap)
		}
	}

	// (c) Pacing genuinely engaged: 25 writes at 10/s span ~2.4s, not ~0.
	if span := ts[len(ts)-1].Sub(ts[0]); span < 2300*time.Millisecond {
		t.Fatalf("total span = %v, want >= 2.3s (throttle did not engage)", span)
	}
}

// TestLimiterSharedAcrossClients proves a single Limiter instance shared by two
// clients caps their COMBINED outbound rate — backfill + event worker can't
// collectively exceed the ceiling.
func TestLimiterSharedAcrossClients(t *testing.T) {
	fc := newFakeClock()
	shared := limiterOnClock(fc, 10, 1)
	rt := &recordRT{clock: fc}

	mk := func() *Client {
		c := New("http://sedoc.test/api/v1", "vdms_test").WithLimiter(shared)
		c.hc = &http.Client{Transport: rt}
		return c
	}
	worker, backfill := mk(), mk()

	const each = 10 // 20 combined writes, interleaved
	for i := 0; i < each; i++ {
		if err := worker.doJSON(context.Background(), http.MethodPost, "/ingest", "", nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := backfill.doJSON(context.Background(), http.MethodPost, "/workspaces/ws/documents:upsert", "", nil, nil); err != nil {
			t.Fatal(err)
		}
	}

	ts := rt.times()
	if len(ts) != 2*each {
		t.Fatalf("recorded %d combined writes, want %d", len(ts), 2*each)
	}
	if got := maxInWindow(ts, time.Second); got > 11 {
		t.Fatalf("combined max writes in any 1s window = %d, want <= 11", got)
	}
}

// TestLimiterStats checks the getter Prompt 6 will chart from.
func TestLimiterStats(t *testing.T) {
	fc := newFakeClock()
	lim := limiterOnClock(fc, 10, 1)

	// First call uses the single burst token (no wait); the next 4 each wait 100ms.
	for i := 0; i < 5; i++ {
		if err := lim.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	s := lim.Stats()
	if s.LimitPerSec != 10 {
		t.Fatalf("LimitPerSec = %v, want 10", s.LimitPerSec)
	}
	if s.Burst != 1 {
		t.Fatalf("Burst = %d, want 1", s.Burst)
	}
	if s.WaitCount != 4 {
		t.Fatalf("WaitCount = %d, want 4", s.WaitCount)
	}
	if s.WaitTotal != 400*time.Millisecond {
		t.Fatalf("WaitTotal = %v, want 400ms", s.WaitTotal)
	}
	if s.TokensAvailable > 1.0001 {
		t.Fatalf("TokensAvailable = %v, want <= burst (1)", s.TokensAvailable)
	}
}

// TestLimiterReadsNotThrottledAndNilSafe confirms GETs are exempt, and a client
// with no limiter behaves exactly as before (no pacing, no panic).
func TestLimiterReadsNotThrottledAndNilSafe(t *testing.T) {
	fc := newFakeClock()
	rt := &recordRT{clock: fc}

	// Limited client: a long run of GETs must not advance the clock at all.
	c := New("http://sedoc.test/api/v1", "vdms_test").WithLimiter(limiterOnClock(fc, 10, 1))
	c.hc = &http.Client{Transport: rt}
	for i := 0; i < 50; i++ {
		if err := c.doJSON(context.Background(), http.MethodGet, "/folders/x", "", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if !fc.now().Equal(time.Unix(0, 0)) {
		t.Fatalf("reads advanced the clock to %v — GETs should be unthrottled", fc.now())
	}

	// Nil-limiter client: writes proceed without pacing (unchanged behavior).
	rt2 := &recordRT{clock: fc}
	plain := New("http://sedoc.test/api/v1", "vdms_test")
	plain.hc = &http.Client{Transport: rt2}
	for i := 0; i < 30; i++ {
		if err := plain.doJSON(context.Background(), http.MethodPost, "/ingest", "", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := maxInWindow(rt2.times(), time.Second); got != 30 {
		t.Fatalf("nil-limiter writes in 1s window = %d, want 30 (unthrottled)", got)
	}
}

// TestLimiterCancelReturnsContextErr ensures a cancelled context surfaces during
// a paced wait rather than hanging or silently dropping the call — so the
// worker's retry/DLQ accounting is unchanged on shutdown.
func TestLimiterCancelReturnsContextErr(t *testing.T) {
	// Real-clock limiter with a long interval; the first token is free, the
	// second forces a wait we cancel out from under.
	lim := NewLimiter(1, 1) // 1/s
	if err := lim.Wait(context.Background()); err != nil {
		t.Fatalf("first wait: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := lim.Wait(ctx); err == nil {
		t.Fatal("expected context error from cancelled wait")
	}
}
