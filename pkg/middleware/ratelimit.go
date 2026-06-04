package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/aieera/sedoc/pkg/auth"
)

// RateLimiter is a token-bucket rate limiter backed by Redis. Keys are of
// the form "ratelimit:{tenant}:{group}". A per-tenant override may be stored
// under "ratelimit:config:{tenant}:{group}" as an integer (requests/min);
// otherwise the default applies.
type RateLimiter struct {
	rdb          *redis.Client
	defaultPerMin int
}

// NewRateLimiter constructs a limiter.
func NewRateLimiter(rdb *redis.Client, defaultPerMin int) *RateLimiter {
	if defaultPerMin <= 0 {
		defaultPerMin = 1000
	}
	return &RateLimiter{rdb: rdb, defaultPerMin: defaultPerMin}
}

// Allow atomically charges 1 token against the bucket for tenant+group.
// Returns (allowed, retryAfter). On Redis failure it fails open (allowed=true)
// to avoid taking the whole tier down if the cache is unhealthy.
func (r *RateLimiter) Allow(ctx context.Context, tenantID, group string) (bool, time.Duration) {
	limit, err := r.limit(ctx, tenantID, group)
	if err != nil {
		return true, 0
	}
	// Simple fixed-window: INCR a minute-bucketed key with EXPIRE 60s.
	bucket := time.Now().UTC().Truncate(time.Minute).Unix()
	key := fmt.Sprintf("ratelimit:%s:%s:%d", tenantID, group, bucket)
	count, err := r.rdb.Incr(ctx, key).Result()
	if err != nil {
		return true, 0
	}
	if count == 1 {
		_ = r.rdb.Expire(ctx, key, 65*time.Second).Err()
	}
	if count > int64(limit) {
		// Seconds remaining in the current minute.
		retry := time.Until(time.Now().UTC().Truncate(time.Minute).Add(time.Minute))
		return false, retry
	}
	return true, 0
}

func (r *RateLimiter) limit(ctx context.Context, tenantID, group string) (int, error) {
	val, err := r.rdb.Get(ctx, fmt.Sprintf("ratelimit:config:%s:%s", tenantID, group)).Result()
	if err == redis.Nil {
		return r.defaultPerMin, nil
	}
	if err != nil {
		return r.defaultPerMin, err
	}
	n, err := strconv.Atoi(val)
	if err != nil || n <= 0 {
		return r.defaultPerMin, nil
	}
	return n, nil
}

// RateLimitHTTP returns middleware that rate-limits per tenant + endpoint group.
//
// If no tenant is on the context (e.g. pre-auth endpoints like /auth/login,
// /auth/register, /auth/mfa/verify) the middleware is a no-op — those paths
// are rate-limited separately by NewIPRateLimiter in the auth router itself,
// so they never reach the tenant-keyed quota.
func RateLimitHTTP(rl *RateLimiter, group string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tid, err := auth.GetTenantID(r.Context())
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			ok, retry := rl.Allow(r.Context(), tid.String(), group)
			if !ok {
				w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
				writeJSON(w, http.StatusTooManyRequests, map[string]any{
					"type":           "RATE_LIMITED",
					"message":        "rate limit exceeded",
					"correlation_id": auth.GetCorrelationID(r.Context()),
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RateLimitPerTenantHTTP is RateLimitHTTP for routes that are ALWAYS
// authenticated before they reach here — API-key / iPaaS poll-trigger
// surfaces (ADR 0090) and the MCP server. It MUST be wrapped by the auth
// middleware (APIKeyAuth / SessionOrAPIKey) so the tenant the key resolved
// to is on the context when it runs.
//
// Unlike RateLimitHTTP it does NOT no-op when the tenant is absent: on an
// always-authenticated route a missing tenant can only mean a wiring bug or
// a bypass attempt, so it fails CLOSED (500) rather than silently disabling
// the per-tenant quota. Allow() itself still fails OPEN on Redis errors, so a
// cache blip can't take ERP/iPaaS ingest down — only the per-tenant cap
// lapses, with Kong's global ceiling as the backstop.
func RateLimitPerTenantHTTP(rl *RateLimiter, group string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tid, err := auth.GetTenantID(r.Context())
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{
					"type":           "INTERNAL",
					"message":        "rate limiter requires tenant context",
					"correlation_id": auth.GetCorrelationID(r.Context()),
				})
				return
			}
			ok, retry := rl.Allow(r.Context(), tid.String(), group)
			if !ok {
				w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
				writeJSON(w, http.StatusTooManyRequests, map[string]any{
					"type":           "RATE_LIMITED",
					"message":        "rate limit exceeded",
					"correlation_id": auth.GetCorrelationID(r.Context()),
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
