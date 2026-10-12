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
// so rotating that header gave every request a fresh in-process rate-limit
// bucket (login, MFA verify, password reset). RemoteAddr must stay the
// peer address.
func TestAuthRouter_TrueClientIPIgnored(t *testing.T) {
	router := New(Config{Logger: zerolog.Nop()}).Router(nil, nil, nil, nil, nil, nil, nil, nil, nil)

	limited := false
	for i := 0; i < 12; i++ { // login limiter: 5 per window per peer
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{`))
		req.RemoteAddr = "192.0.2.10:40000"
		req.Header.Set("True-Client-IP", fmt.Sprintf("198.51.100.%d", i+1))
		req.Header.Set("X-Real-IP", fmt.Sprintf("203.0.113.%d", i+1))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("rotating True-Client-IP / X-Real-IP must not reset the per-peer login rate limit")
	}
}
