package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/pkg/auth"
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
		name     string
		tenantID string
		userID   string
		want     int
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

// The auth-url stub era ended with ADR 0111: google (and m365 via its
// dedicated route) run real OAuth, and every other provider is rejected
// up front. These tests pin the validation layer that runs BEFORE the
// service call — newMux wires a nil svc, so reaching svc would panic,
// which is itself a guard that validation stays in front.

func TestGetAuthURL_RequiresTenant(t *testing.T) {
	mux := newMux(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/connectors/google/auth-url", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("tenant required")) {
		t.Errorf("response missing tenant error: %s", w.Body.String())
	}
}

func TestGetAuthURL_RejectsUnsupportedProvider(t *testing.T) {
	mux := newMux(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/connectors/salesforce/auth-url", nil)
	// getAuthURL reads the tenant from the auth context (stamped by the
	// middleware newMux skips), not the raw header — inject it directly.
	req = req.WithContext(auth.SetTenantID(req.Context(), uuid.New()))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("provider not yet supported: salesforce")) {
		t.Errorf("response missing unsupported-provider error: %s", w.Body.String())
	}
}

func TestOAuthCallback_RequiresCodeAndState(t *testing.T) {
	// The legacy per-provider callback delegates to the unified handler,
	// which rejects before any provider dispatch when code/state are
	// missing (the HMAC-signed state is the provider authority).
	mux := newMux(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/connectors/google/callback", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("code and state required")) {
		t.Errorf("response missing code/state error: %s", w.Body.String())
	}
}
