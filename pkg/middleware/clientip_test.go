package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aieera/sedoc/pkg/auth"
)

func reqFrom(remote string, xff ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	for _, v := range xff {
		r.Header.Add("X-Forwarded-For", v)
	}
	return r
}

// TestClientIP_RightmostUntrustedHop pins DEC-5 / R-2: behind a trusted
// proxy the client IP is the right-most X-Forwarded-For hop that is not
// itself a trusted proxy. Kong appends the connecting address, so every
// hop to the left of it is client-controlled and must not win.
func TestClientIP_RightmostUntrustedHop(t *testing.T) {
	t.Setenv(TrustedProxiesEnv, "10.0.0.0/8, 172.16.0.0/12,192.0.2.10")
	cases := []struct {
		name   string
		remote string
		xff    []string
		want   string
	}{
		{"forged left-most hop ignored", "10.0.0.5:4000", []string{"1.2.3.4, 203.0.113.9"}, "203.0.113.9"},
		{"trusted hops skipped", "10.0.0.5:4000", []string{"1.2.3.4, 203.0.113.9, 172.16.0.3, 10.1.1.1"}, "203.0.113.9"},
		{"multiple header lines", "10.0.0.5:4000", []string{"1.2.3.4", "203.0.113.9, 192.0.2.10"}, "203.0.113.9"},
		{"ipv6 hop", "10.0.0.5:4000", []string{"2001:db8::1"}, "2001:db8::1"},
		{"no XFF falls back to remote", "10.0.0.5:4000", nil, "10.0.0.5"},
		{"all hops trusted falls back to remote", "10.0.0.5:4000", []string{"10.9.9.9"}, "10.0.0.5"},
		{"garbage right-most hop falls back to remote", "10.0.0.5:4000", []string{"1.2.3.4, not-an-ip"}, "10.0.0.5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClientIP(reqFrom(tc.remote, tc.xff...)); got != tc.want {
				t.Errorf("ClientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestClientIP_UntrustedRemoteIgnoresXFF: when the direct peer is not a
// trusted proxy (default: no proxies trusted) X-Forwarded-For and
// X-Real-IP are attacker-supplied and ignored entirely.
func TestClientIP_UntrustedRemoteIgnoresXFF(t *testing.T) {
	t.Run("no trusted proxies configured", func(t *testing.T) {
		t.Setenv(TrustedProxiesEnv, "")
		r := reqFrom("198.51.100.7:5555", "1.2.3.4")
		r.Header.Set("X-Real-IP", "5.6.7.8")
		if got := ClientIP(r); got != "198.51.100.7" {
			t.Errorf("ClientIP = %q, want 198.51.100.7", got)
		}
	})
	t.Run("remote outside the trusted set", func(t *testing.T) {
		t.Setenv(TrustedProxiesEnv, "10.0.0.0/8")
		if got := ClientIP(reqFrom("198.51.100.7:5555", "1.2.3.4")); got != "198.51.100.7" {
			t.Errorf("ClientIP = %q, want 198.51.100.7", got)
		}
	})
	t.Run("ipv6 remote", func(t *testing.T) {
		t.Setenv(TrustedProxiesEnv, "")
		if got := ClientIP(reqFrom("[2001:db8::7]:443", "1.2.3.4")); got != "2001:db8::7" {
			t.Errorf("ClientIP = %q, want 2001:db8::7", got)
		}
	})
	t.Run("correlation stamps ClientIP on ctx", func(t *testing.T) {
		t.Setenv(TrustedProxiesEnv, "")
		var got string
		CorrelationHTTP(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			got = auth.GetClientIP(r.Context())
		})).ServeHTTP(httptest.NewRecorder(), reqFrom("198.51.100.7:5555", "1.2.3.4"))
		if got != "198.51.100.7" {
			t.Errorf("ctx client IP = %q, want 198.51.100.7", got)
		}
	})
}

// TestIPRateLimiter_RefillsAndKeysPerClient pins R-1: the bucket is keyed
// by ClientIP (so clients behind one proxy no longer share a bucket) and
// refills at `rate` tokens per `window` (the old limiter never read rate
// and only reset after a full idle window).
func TestIPRateLimiter_RefillsAndKeysPerClient(t *testing.T) {
	t.Setenv(TrustedProxiesEnv, "10.0.0.0/8")
	now := time.Unix(1_700_000_000, 0)
	l := newIPLimiter(6, 3, time.Minute) // 6/min = one token per 10s, burst 3
	l.now = func() time.Time { return now }
	h := l.limit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	hit := func(client string) int {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, reqFrom("10.0.0.2:9999", client)) // all via the same proxy
		return rr.Code
	}

	for i := 0; i < 3; i++ {
		if c := hit("203.0.113.1"); c != http.StatusOK {
			t.Fatalf("burst request %d: got %d, want 200", i+1, c)
		}
	}
	if c := hit("203.0.113.1"); c != http.StatusTooManyRequests {
		t.Fatalf("over burst: got %d, want 429", c)
	}
	// A different client behind the same proxy has its own bucket.
	if c := hit("203.0.113.2"); c != http.StatusOK {
		t.Fatalf("second client: got %d, want 200 (buckets must be per client)", c)
	}
	// Continuous hammering inside the window must not reset the bucket.
	now = now.Add(5 * time.Second)
	if c := hit("203.0.113.1"); c != http.StatusTooManyRequests {
		t.Fatalf("after 5s: got %d, want 429 (only half a token refilled)", c)
	}
	// rate=6/min → one token every 10s.
	now = now.Add(10 * time.Second)
	if c := hit("203.0.113.1"); c != http.StatusOK {
		t.Fatalf("after refill: got %d, want 200", c)
	}
	if c := hit("203.0.113.1"); c != http.StatusTooManyRequests {
		t.Fatalf("refill is one token, got %d, want 429", c)
	}
	// Refill is capped at burst.
	now = now.Add(time.Hour)
	for i := 0; i < 3; i++ {
		if c := hit("203.0.113.1"); c != http.StatusOK {
			t.Fatalf("post-idle burst %d: got %d, want 200", i+1, c)
		}
	}
	if c := hit("203.0.113.1"); c != http.StatusTooManyRequests {
		t.Fatalf("burst cap after idle: got %d, want 429", c)
	}
}
