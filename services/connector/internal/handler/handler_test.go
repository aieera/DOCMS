package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newMux(t *testing.T) *http.ServeMux {
	t.Helper()
	// mcpSrv is nil; we only exercise the webhook/connector HTTP routes.
	h := &Handler{}
	mux := http.NewServeMux()
	// Re-register by hand to skip mcpSrv wiring.
	mux.HandleFunc("POST /api/v1/webhooks", h.createWebhook)
	mux.HandleFunc("GET /api/v1/webhooks", h.listWebhooks)
	mux.HandleFunc("DELETE /api/v1/webhooks/{id}", h.deleteWebhook)
	mux.HandleFunc("GET /api/v1/webhooks/{id}/deliveries", h.getDeliveryLog)
	mux.HandleFunc("POST /api/v1/webhooks/{id}/rotate-secret", h.rotateSecret)
	mux.HandleFunc("POST /api/v1/webhooks/{id}/deliveries/{deliveryId}/redeliver", h.redeliverDelivery)
	mux.HandleFunc("GET /api/v1/connectors", h.listConnectors)
	mux.HandleFunc("GET /api/v1/connectors/{provider}", h.getConnector)
	mux.HandleFunc("GET /api/v1/connectors/{provider}/auth-url", h.getAuthURL)
	mux.HandleFunc("POST /api/v1/connectors/{provider}/callback", h.oauthCallback)
	return mux
}

func TestCreateWebhook_RequiresHeaders(t *testing.T) {
	cases := []struct {
		name       string
		tenantID   string
		userID     string
		want       int
	}{
		{"no headers", "", "", http.StatusBadRequest},
		{"only tenant", "t1", "", http.StatusBadRequest},
		{"only user", "", "u1", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := newMux(t)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks",
				bytes.NewBufferString(`{"url":"https://example.com","events":["doc.created"]}`))
			if tc.tenantID != "" {
				req.Header.Set("X-Tenant-ID", tc.tenantID)
			}
			if tc.userID != "" {
				req.Header.Set("X-User-ID", tc.userID)
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Errorf("want %d, got %d", tc.want, w.Code)
			}
		})
	}
}

func TestCreateWebhook_RequiresBodyFields(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"empty body", ``},
		{"malformed", `{{{`},
		{"empty url", `{"url":"","events":["doc.created"]}`},
		{"empty events", `{"url":"https://example.com","events":[]}`},
		{"missing events field", `{"url":"https://example.com"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := newMux(t)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks", bytes.NewBufferString(tc.body))
			req.Header.Set("X-Tenant-ID", "t1")
			req.Header.Set("X-User-ID", "u1")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("want 400, got %d", w.Code)
			}
		})
	}
}

// /auth-url no longer stubs. It now requires a tenant header, a
// redirect_uri, and a provider the service has AttachOAuth'd.
// Without AttachOAuth the call surfaces the misconfig as 400 —
// regression guard that the stub is gone and the error path reports
// clearly instead of silently echoing.
func TestGetAuthURL_RejectsMissingTenant(t *testing.T) {
	mux := newMux(t)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/connectors/salesforce/auth-url?redirect_uri=https://app/cb", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetAuthURL_RejectsMissingRedirectURI(t *testing.T) {
	mux := newMux(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/connectors/salesforce/auth-url", nil)
	req.Header.Set("X-Auth-Tenant-ID", "00000000-0000-0000-0000-000000000001")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestOAuthCallback_RejectsMissingStateAndCode(t *testing.T) {
	mux := newMux(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/connectors/google/callback", nil)
	req.Body = http.NoBody
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body.String())
	}
}
