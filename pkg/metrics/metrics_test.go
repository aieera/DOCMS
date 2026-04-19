package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// normalizePath keeps Prometheus cardinality bounded — without it the
// HTTP metric explodes one label per UUID. Regression here is a silent
// cost explosion, not a correctness bug, so we pin the exact rules.

func TestNormalizePath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/api/v1/documents", "/api/v1/documents"},
		{"/api/v1/documents/aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", "/api/v1/documents/:id"},
		{"/api/v1/documents/019d99f0-c34a-7c0b-badf-aa3f7a14f08b/versions", "/api/v1/documents/:id/versions"},
		{"/api/v1/users/12345", "/api/v1/users/:id"},
		{"/api/v1/users/12345/suspend", "/api/v1/users/:id/suspend"},
		{"/", "/"},
		{"", ""},
		// UUID detection: 36 chars with dash at position 8.
		{"/short", "/short"},
		{"/abcdefghijklmnopqrstuvwxyz123456789x", "/abcdefghijklmnopqrstuvwxyz123456789x"}, // 36 chars but no dash in position 8
	}
	for _, tc := range cases {
		if got := normalizePath(tc.in); got != tc.want {
			t.Errorf("normalizePath(%q): got %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsNumeric(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"123", true},
		{"0", true},
		{"12a", false},
		{"", false},
		{"abc", false},
		{"-1", false},
	}
	for _, tc := range cases {
		if got := isNumeric(tc.in); got != tc.want {
			t.Errorf("isNumeric(%q): got %v, want %v", tc.in, got, tc.want)
		}
	}
}

// HTTPMiddleware wraps the handler and emits counter+histogram. We assert
// that the middleware passes through without corrupting status/body.

func TestHTTPMiddleware_Passthrough(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("ok"))
	})
	mw := HTTPMiddleware(next)
	req := httptest.NewRequest("GET", "/api/v1/test", nil)
	req.Header.Set("X-Tenant-ID", "t-abc")
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("status passthrough: got %d, want 202", w.Code)
	}
	if w.Body.String() != "ok" {
		t.Errorf("body passthrough: got %q", w.Body.String())
	}
}

func TestHTTPMiddleware_AnonTenantWhenHeaderMissing(t *testing.T) {
	// No X-Tenant-ID → the label becomes "_anon". Pin the contract so
	// Prometheus queries that filter on tenant can rely on the sentinel.
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := HTTPMiddleware(next)
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	// We can't easily inspect the Prometheus counter without registering
	// a separate collector — but we can scrape /metrics and check the
	// "_anon" label landed somewhere.
	scrape := httptest.NewRecorder()
	Handler().ServeHTTP(scrape, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(scrape.Body.String(), `tenant_id="_anon"`) {
		t.Error("anon tenant label should be present after request")
	}
}

func TestHandler_ExposesPrometheusFormat(t *testing.T) {
	w := httptest.NewRecorder()
	Handler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") && !strings.HasPrefix(ct, "application/openmetrics-text") {
		t.Errorf("Content-Type: %q (expected Prometheus text)", ct)
	}
	if !strings.Contains(w.Body.String(), "# HELP ") {
		t.Error("scrape should include HELP lines")
	}
}
