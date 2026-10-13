package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aieera/sedoc/pkg/middleware"
)

// DEC-19: the GraphQL route caps the body at 1 MiB before SessionAuth, so an
// oversized body is refused with 413 before the session lookup.
func TestAPI_413OverLimit(t *testing.T) {
	authCalled := false
	denyAll := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authCalled = true
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
	pass := func(next http.Handler) http.Handler { return next }
	root := rootHandler(http.NotFoundHandler(), pass, pass, denyAll, pass)

	big := strings.Repeat("a", int(middleware.MaxJSONBody)+1)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/graphql", strings.NewReader(big))
	rec := httptest.NewRecorder()
	root.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", rec.Code)
	}
	if authCalled {
		t.Fatal("oversized body reached SessionAuth")
	}
}

func TestHTTPServerTimeouts(t *testing.T) {
	s := newHTTPServer(":0", http.NotFoundHandler())
	if s.ReadTimeout != readTimeout || s.WriteTimeout != writeTimeout || s.IdleTimeout != idleTimeout || s.ReadHeaderTimeout == 0 {
		t.Fatalf("timeouts = read %v write %v idle %v header %v", s.ReadTimeout, s.WriteTimeout, s.IdleTimeout, s.ReadHeaderTimeout)
	}
}
