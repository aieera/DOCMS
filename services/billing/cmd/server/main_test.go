package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aieera/sedoc/pkg/middleware"
)

// DEC-19: billing routes (internal API-key surface, Stripe webhook, admin
// settings) cap the body at 1 MiB before any auth runs.
func TestAPI_413OverLimit(t *testing.T) {
	reached := ""
	deny := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached = name
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
	root := apiRoutes(deny("internal"), deny("admin"))

	big := strings.Repeat("a", int(middleware.MaxJSONBody)+1)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/internal/v1/tenants/provision"},
		{http.MethodPost, "/internal/v1/stripe/webhook"},
		{http.MethodPut, "/api/v1/admin/settings"},
	} {
		reached = ""
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(big))
		rec := httptest.NewRecorder()
		root.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s %s: status %d, want 413", tc.method, tc.path, rec.Code)
		}
		if reached != "" {
			t.Errorf("%s %s: oversized body reached the %s handler/auth", tc.method, tc.path, reached)
		}
	}
}

func TestHTTPServerTimeouts(t *testing.T) {
	s := newHTTPServer(":0", http.NotFoundHandler())
	if s.ReadTimeout != readTimeout || s.WriteTimeout != writeTimeout || s.IdleTimeout != idleTimeout || s.ReadHeaderTimeout == 0 {
		t.Fatalf("timeouts = read %v write %v idle %v header %v", s.ReadTimeout, s.WriteTimeout, s.IdleTimeout, s.ReadHeaderTimeout)
	}
}
