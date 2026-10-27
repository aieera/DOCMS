package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aieera/sedoc/pkg/logger"
	"github.com/aieera/sedoc/pkg/middleware"
)

// DEC-19: task routes cap the body at 1 MiB before SessionAuth.
func TestAPI_413OverLimit(t *testing.T) {
	authCalled := false
	denyAll := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authCalled = true
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
	root := apiHandler(http.NotFoundHandler(), denyAll)

	big := strings.Repeat("a", int(middleware.MaxJSONBody)+1)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/tasks", strings.NewReader(big))
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

// I-2: the policy client is dialed lazily (grpc.NewClient) — boot must not
// block on, or fail because of, a policy service that is not up yet.
func TestDialPolicy(t *testing.T) {
	log := logger.New("task-test", "test", "error")
	if c := dialPolicy(context.Background(), log, ""); c != nil {
		t.Fatal("empty POLICY_SERVICE_ADDR must leave the client unset (fail closed)")
	}
	c := dialPolicy(context.Background(), log, "127.0.0.1:1") // nothing listens there
	if c == nil {
		t.Fatal("a lazy dial must return a connection even before policy is reachable")
	}
	_ = c.Close()
}
