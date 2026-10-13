package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/middleware"
	"github.com/aieera/sedoc/services/mcp-server/internal/handler"
	"github.com/aieera/sedoc/services/mcp-server/internal/mcp"
)

// R-5 / DEC-19: the MCP routes cap request bodies at 1 MiB before auth, so
// an oversized body is refused with 413 without being read or reaching the
// key lookup.
func TestAPI_413OverLimit(t *testing.T) {
	mux := http.NewServeMux()
	handler.New(mcp.New(mcp.Config{Logger: zerolog.Nop()}), zerolog.Nop()).Register(mux)
	authCalled := false
	denyAll := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authCalled = true
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
	pass := func(next http.Handler) http.Handler { return next }
	root := mcpRoutes(mux, denyAll, pass, pass)

	big := strings.Repeat("a", int(middleware.MaxJSONBody)+1)
	for _, path := range []string{"/api/v1/mcp", "/api/v1/mcp/sse?session_id=x"} {
		authCalled = false
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(big))
		rec := httptest.NewRecorder()
		root.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("POST %s with %d bytes: status %d, want 413", path, len(big), rec.Code)
		}
		if authCalled {
			t.Errorf("POST %s: oversized body reached auth", path)
		}
	}

	// A chunked (unknown-length) oversized body that gets past the header
	// check still fails with 413 from the handler's decode.
	root = mcpRoutes(mux, pass, pass, pass)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"`+big+`"}`))
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	root.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("chunked oversized body: status %d, want 413", rec.Code)
	}
}

func TestHTTPServerTimeouts(t *testing.T) {
	s := newHTTPServer(":0", http.NotFoundHandler())
	if s.ReadTimeout != readTimeout || s.WriteTimeout != writeTimeout || s.IdleTimeout != idleTimeout || s.ReadHeaderTimeout == 0 {
		t.Fatalf("timeouts = read %v write %v idle %v header %v", s.ReadTimeout, s.WriteTimeout, s.IdleTimeout, s.ReadHeaderTimeout)
	}
}
