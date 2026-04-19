package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vaultdms/vaultdms/pkg/auth"
)

// Correlation-id propagation is how every log line gets threaded to the
// originating request. If a hop drops it, an incident becomes unreadable.

func TestCorrelationHTTP_GeneratesWhenAbsent(t *testing.T) {
	var seen string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = auth.GetCorrelationID(r.Context())
	})
	req := httptest.NewRequest("GET", "/x", nil)
	w := httptest.NewRecorder()
	CorrelationHTTP(next).ServeHTTP(w, req)

	if seen == "" {
		t.Fatal("generated id should be on ctx")
	}
	if got := w.Header().Get(CorrelationHeader); got != seen {
		t.Errorf("response header mismatch: header=%q ctx=%q", got, seen)
	}
}

func TestCorrelationHTTP_PreservesIncoming(t *testing.T) {
	want := "incoming-corr-id"
	var seen string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = auth.GetCorrelationID(r.Context())
	})
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set(CorrelationHeader, want)
	w := httptest.NewRecorder()
	CorrelationHTTP(next).ServeHTTP(w, req)

	if seen != want {
		t.Errorf("downstream ctx: got %q, want %q", seen, want)
	}
	if w.Header().Get(CorrelationHeader) != want {
		t.Error("response must echo the same correlation id")
	}
}

// ---- RateLimitHTTP: pre-auth bypass -------------------------------------

func TestRateLimitHTTP_NoTenantIsNoop(t *testing.T) {
	// Regression guard for remediation 04b: the pre-auth /auth/login path
	// must NOT be gated by a tenant-keyed rate limit. If the middleware
	// 401s on missing tenant we can't even log in.
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	mw := RateLimitHTTP(nil, "auth") // nil limiter is OK — we don't reach Allow
	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	w := httptest.NewRecorder()
	mw(next).ServeHTTP(w, req)

	if !called {
		t.Fatal("pre-auth path was blocked by rate limiter")
	}
	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
}

// ---- newID is a stable UUIDv7 generator used for correlation ids --------

func TestNewID_Unique(t *testing.T) {
	seen := map[string]struct{}{}
	for i := 0; i < 100; i++ {
		id := newID()
		if id == "" {
			t.Fatal("empty id")
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id: %s", id)
		}
		seen[id] = struct{}{}
	}
}
