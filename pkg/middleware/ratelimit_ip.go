package middleware

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// ipLimiter is a per-client token bucket: each client (keyed by ClientIP)
// holds up to `burst` tokens, refilled continuously at `rate` tokens per
// `window`; a request spends one token or gets 429.
type ipLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	burst    float64
	perSec   float64 // refill rate in tokens per second
	now      func() time.Time
}

type visitor struct {
	tokens   float64
	lastSeen time.Time
}

// NewIPRateLimiter returns a middleware that rate-limits each client IP to
// `rate` requests per `window`, with bursts of up to `burst` (R-1). The
// client is identified by ClientIP, so behind a gateway listed in
// SEDOC_TRUSTED_PROXIES each caller gets its own bucket instead of all of
// them sharing the gateway's address.
func NewIPRateLimiter(rate, burst int, window time.Duration) func(http.Handler) http.Handler {
	l := newIPLimiter(rate, burst, window)
	go l.cleanup()
	return l.limit
}

func newIPLimiter(rate, burst int, window time.Duration) *ipLimiter {
	if burst < 1 {
		burst = 1
	}
	if rate < 1 {
		rate = 1
	}
	if window <= 0 {
		window = time.Minute
	}
	return &ipLimiter{
		visitors: make(map[string]*visitor),
		burst:    float64(burst),
		perSec:   float64(rate) / window.Seconds(),
		now:      time.Now,
	}
}

func (l *ipLimiter) limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ok, wait := l.allow(ClientIP(r)); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// allow spends one token for key, refilling first. When refused it
// reports how long until the next token.
func (l *ipLimiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	v, ok := l.visitors[key]
	if !ok {
		v = &visitor{tokens: l.burst, lastSeen: now}
		l.visitors[key] = v
	} else {
		if elapsed := now.Sub(v.lastSeen).Seconds(); elapsed > 0 {
			v.tokens = math.Min(l.burst, v.tokens+elapsed*l.perSec)
		}
		v.lastSeen = now
	}
	if v.tokens < 1 {
		return false, time.Duration((1 - v.tokens) / l.perSec * float64(time.Second))
	}
	v.tokens--
	return true, 0
}

// cleanup drops buckets idle long enough to have refilled completely —
// their state is identical to a fresh bucket, so forgetting them is
// lossless and bounds memory.
func (l *ipLimiter) cleanup() {
	full := time.Duration(l.burst / l.perSec * float64(time.Second))
	if full < time.Minute {
		full = time.Minute
	}
	t := time.NewTicker(full)
	defer t.Stop()
	for range t.C {
		l.mu.Lock()
		now := l.now()
		for k, v := range l.visitors {
			if now.Sub(v.lastSeen) >= full {
				delete(l.visitors, k)
			}
		}
		l.mu.Unlock()
	}
}
