// Package gateway provides cross-cutting HTTP middleware: rate limiting,
// WAF patterns, security headers, CORS, and body size limits.
package gateway

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// RateLimiter implements a Redis token bucket per tenant+IP+endpoint.
type RateLimiter struct {
	rdb          *redis.Client
	ratePerMin   int
	burstSize    int
}

// NewRateLimiter creates a rate limiter.
func NewRateLimiter(rdb *redis.Client, ratePerMin, burstSize int) *RateLimiter {
	return &RateLimiter{rdb: rdb, ratePerMin: ratePerMin, burstSize: burstSize}
}

// Allow checks if the request is within rate limits.
func (rl *RateLimiter) Allow(ctx context.Context, tenantID, ip, endpoint string) (bool, int, error) {
	key := fmt.Sprintf("rl:%s:%s:%s", tenantID, ip, endpoint)
	pipe := rl.rdb.Pipeline()
	incr := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, 60*time.Second)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return true, 0, err // fail open on Redis errors
	}
	count := int(incr.Val())
	remaining := rl.burstSize - count
	if remaining < 0 {
		remaining = 0
	}
	return count <= rl.burstSize, remaining, nil
}

// Middleware returns an HTTP middleware that enforces rate limits.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Track 3 — canonical X-Auth-Tenant-ID, legacy X-Tenant-ID fallback.
		tenantID := r.Header.Get("X-Auth-Tenant-ID")
		if tenantID == "" {
			tenantID = r.Header.Get("X-Tenant-ID")
		}
		if tenantID == "" {
			tenantID = "_anon"
		}
		ip := extractIP(r)
		endpoint := r.Method + ":" + r.URL.Path
		allowed, remaining, _ := rl.Allow(r.Context(), tenantID, ip, endpoint)
		w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))
		if !allowed {
			w.Header().Set("Retry-After", "60")
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func extractIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}
