package sedoc

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter is a proactive, shared token-bucket throttle for outbound SeDoc writes.
//
// SeDoc rate-limits :upsert and /ingest at 600/min per API key (INTEGRATION.md
// §2). The worker's reactive 429 + Retry-After backoff only engages AFTER the
// ceiling is breached — under an event burst or a backfill that means a storm of
// rejected writes cascading into retries and the DLQ. This limiter paces writes
// BEFORE they leave, so the ceiling is respected proactively; the 429 backoff
// stays as a defensive second layer.
//
// A SINGLE Limiter instance is meant to be shared across every writer (the event
// worker and the backfill) so they cannot collectively exceed the ceiling. It is
// safe for concurrent use.
type Limiter struct {
	rl *rate.Limiter

	// now/sleep are injectable so tests can drive the limiter on a fake clock
	// without real wall-clock delays. In production they are time.Now and a
	// context-aware sleep.
	now   func() time.Time
	sleep func(context.Context, time.Duration) error

	mu        sync.Mutex
	waitCount int64
	waitTotal time.Duration
}

// NewLimiter builds a shared limiter at perSec events/second with the given
// burst. For SeDoc's 600/min ceiling pass perSec=10; burst allows a small spike
// above the steady rate before pacing engages. Non-positive perSec falls back to
// 10/s and burst is floored at 1.
func NewLimiter(perSec float64, burst int) *Limiter {
	if perSec <= 0 {
		perSec = 10
	}
	if burst < 1 {
		burst = 1
	}
	return &Limiter{
		rl:    rate.NewLimiter(rate.Limit(perSec), burst),
		now:   time.Now,
		sleep: sleepCtx,
	}
}

// Wait blocks until a token is available (or ctx is done), pacing the caller to
// the configured rate, and records the wait for Stats(). A token is only
// consumed when the call proceeds — if ctx is cancelled mid-wait the reservation
// is returned so the budget isn't silently burned on shutdown.
//
// It is implemented over Reserve (rather than rate.Limiter.Wait) so the exact
// delay is observable for metrics and so the clock can be stubbed in tests.
func (l *Limiter) Wait(ctx context.Context) error {
	t := l.now()
	r := l.rl.ReserveN(t, 1)
	if !r.OK() {
		// Unreachable while burst >= 1 (NewLimiter guarantees it); guard anyway.
		return fmt.Errorf("sedoc: rate limiter cannot satisfy a single event (burst too small)")
	}
	d := r.DelayFrom(t)
	if d <= 0 {
		return nil
	}
	if err := l.sleep(ctx, d); err != nil {
		r.CancelAt(l.now()) // hand the token back; the call never went out
		return err
	}
	l.mu.Lock()
	l.waitCount++
	l.waitTotal += d
	l.mu.Unlock()
	return nil
}

// LimiterStats is a snapshot of the limiter for metrics/charting (Prompt 6).
type LimiterStats struct {
	LimitPerSec     float64       `json:"limit_per_sec"`
	Burst           int           `json:"burst"`
	TokensAvailable float64       `json:"tokens_available"`
	WaitCount       int64         `json:"wait_count"`
	WaitTotal       time.Duration `json:"wait_total"`
}

// Stats returns a snapshot: the configured rate/burst, tokens available right
// now, and the cumulative number of paced waits plus total time spent waiting.
// Safe to call concurrently with Wait.
func (l *Limiter) Stats() LimiterStats {
	l.mu.Lock()
	wc, wt := l.waitCount, l.waitTotal
	l.mu.Unlock()
	return LimiterStats{
		LimitPerSec:     float64(l.rl.Limit()),
		Burst:           l.rl.Burst(),
		TokensAvailable: l.rl.TokensAt(l.now()),
		WaitCount:       wc,
		WaitTotal:       wt,
	}
}

// sleepCtx sleeps for d unless ctx is cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
