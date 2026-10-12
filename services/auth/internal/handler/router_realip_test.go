package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

// L-5 — chi's RealIP rewrote RemoteAddr from client-supplied True-Client-IP
// / X-Real-IP / X-Forwarded-For, and Kong does not strip True-Client-IP,
// so rotating those headers gave every request a fresh in-process
// rate-limit bucket (login, MFA verify, password reset). The limiter keys
// on the shared trusted-proxy ClientIP: headers from an untrusted peer
// never change the key, and behind a trusted gateway only the hop the
// gateway itself appended does.
func TestAuthRouter_TrueClientIPIgnored(t *testing.T) {
	// login limiter: burst 5 per client key.
	limitedWithin := func(router http.Handler, n int, mutate func(i int, r *http.Request)) bool {
		for i := 0; i < n; i++ {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{`))
			mutate(i, req)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code == http.StatusTooManyRequests {
				return true
			}
		}
		return false
	}
	newRouter := func() http.Handler {
		return New(Config{Logger: zerolog.Nop()}).Router(nil, nil, nil, nil, nil, nil, nil, nil, nil)
	}

	t.Run("untrusted peer: spoofed headers do not change the key", func(t *testing.T) {
		t.Setenv("SEDOC_TRUSTED_PROXIES", "")
		if !limitedWithin(newRouter(), 12, func(i int, r *http.Request) {
			r.RemoteAddr = "192.0.2.10:40000"
			r.Header.Set("True-Client-IP", fmt.Sprintf("198.51.100.%d", i+1))
			r.Header.Set("X-Real-IP", fmt.Sprintf("203.0.113.%d", i+1))
			r.Header.Set("X-Forwarded-For", fmt.Sprintf("198.18.0.%d", i+1))
		}) {
			t.Fatal("rotating True-Client-IP / X-Real-IP / X-Forwarded-For must not reset the per-client login rate limit")
		}
	})

	t.Run("trusted gateway: client-prepended hops do not change the key", func(t *testing.T) {
		t.Setenv("SEDOC_TRUSTED_PROXIES", "10.0.0.0/8")
		if !limitedWithin(newRouter(), 12, func(i int, r *http.Request) {
			r.RemoteAddr = "10.0.0.2:40000" // Kong
			r.Header.Set("True-Client-IP", fmt.Sprintf("198.51.100.%d", i+1))
			r.Header.Set("X-Forwarded-For", fmt.Sprintf("198.18.0.%d, 203.0.113.50", i+1))
		}) {
			t.Fatal("a spoofed left-most X-Forwarded-For entry must not reset the client's bucket")
		}
	})

	t.Run("trusted gateway: distinct clients get distinct buckets", func(t *testing.T) {
		t.Setenv("SEDOC_TRUSTED_PROXIES", "10.0.0.0/8")
		if limitedWithin(newRouter(), 12, func(i int, r *http.Request) {
			r.RemoteAddr = "10.0.0.2:40000"
			r.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d", i+1))
		}) {
			t.Fatal("behind the gateway each real client must have its own bucket, not share Kong's")
		}
	})
}
