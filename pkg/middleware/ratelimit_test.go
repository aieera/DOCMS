package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/aieera/sedoc/pkg/auth"
)

// stampTenant wraps h so the request arrives with `tid` on its context —
// standing in for APIKeyAuth, which is what populates the tenant on the
// real iPaaS routes before the limiter runs.
func stampTenant(tid uuid.UUID, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := auth.WithUser(r.Context(), auth.UserInfo{TenantID: tid})
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

func rlOK() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

func fire(h http.Handler) int {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/integrations/triggers/documents", nil))
	return rec.Code
}

// TestRateLimitPerTenantHTTP_IsolatesTenants is the load-bearing assertion:
// one tenant exhausting its budget must NOT throttle another tenant. The
// buckets are distinct redis keys (ratelimit:{tenant}:{group}:{min}).
func TestRateLimitPerTenantHTTP_IsolatesTenants(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	rl := NewRateLimiter(rdb, 3) // 3/min

	tenantA, tenantB := uuid.New(), uuid.New()
	hitA := stampTenant(tenantA, RateLimitPerTenantHTTP(rl, "integrations")(rlOK()))
	hitB := stampTenant(tenantB, RateLimitPerTenantHTTP(rl, "integrations")(rlOK()))

	// Tenant A: first 3 allowed, 4th throttled.
	for i := 1; i <= 3; i++ {
		if code := fire(hitA); code != http.StatusOK {
			t.Fatalf("tenant A request %d: want 200, got %d", i, code)
		}
	}
	rec := httptest.NewRecorder()
	hitA.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("tenant A request 4: want 429, got %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("429 response missing Retry-After header")
	}

	// Tenant B, SAME minute window: untouched by A's exhaustion.
	for i := 1; i <= 3; i++ {
		if code := fire(hitB); code != http.StatusOK {
			t.Fatalf("tenant B request %d: want 200 (A's quota must not bleed into B), got %d", i, code)
		}
	}
}

// TestRateLimitPerTenantHTTP_FailsClosedWithoutTenant: on an
// always-authenticated route a missing tenant is a bug/bypass, so the
// limiter returns 500 rather than silently passing through.
func TestRateLimitPerTenantHTTP_FailsClosedWithoutTenant(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	rl := NewRateLimiter(rdb, 10)

	// No stampTenant wrapper → no tenant on ctx.
	h := RateLimitPerTenantHTTP(rl, "integrations")(rlOK())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("missing tenant: want 500 (fail closed), got %d", rec.Code)
	}
}

// TestRateLimitPerTenantHTTP_PerTenantOverride: a tenant-specific limit
// stored at ratelimit:config:{tenant}:{group} wins over the default.
func TestRateLimitPerTenantHTTP_PerTenantOverride(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	rl := NewRateLimiter(rdb, 100) // generous default

	tenantA, tenantB := uuid.New(), uuid.New()
	// Throttle only tenant A to 1/min.
	mr.Set("ratelimit:config:"+tenantA.String()+":integrations", "1")

	hitA := stampTenant(tenantA, RateLimitPerTenantHTTP(rl, "integrations")(rlOK()))
	hitB := stampTenant(tenantB, RateLimitPerTenantHTTP(rl, "integrations")(rlOK()))

	if code := fire(hitA); code != http.StatusOK {
		t.Fatalf("tenant A request 1: want 200, got %d", code)
	}
	if code := fire(hitA); code != http.StatusTooManyRequests {
		t.Fatalf("tenant A request 2 (override=1): want 429, got %d", code)
	}
	// Tenant B has no override → still on the generous default.
	for i := 1; i <= 5; i++ {
		if code := fire(hitB); code != http.StatusOK {
			t.Fatalf("tenant B request %d: want 200 (no override), got %d", i, code)
		}
	}
}
