package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/pkg/auth"
)

// gatedMux mounts every connector admin route group through its real
// Register method with nil services: a request that got past the role gate
// would dereference a nil service and panic, so a 403 here proves the gate
// runs before any work.
func gatedMux() *http.ServeMux {
	mux := http.NewServeMux()
	(&Handler{}).Register(mux, nil)
	NewEventStreamHandler(nil).Register(mux)
	NewEmailHandler(nil).Register(mux)
	NewIntakeHandler(nil).Register(mux)
	NewCaptureHandler(nil).Register(mux)
	return mux
}

func asRole(r *http.Request, role string) *http.Request {
	return r.WithContext(auth.WithUser(r.Context(), auth.UserInfo{
		ID: uuid.New(), TenantID: uuid.New(), Role: role,
	}))
}

type route struct{ method, path, body string }

func expectForbidden(t *testing.T, routes []route, roles ...string) {
	t.Helper()
	mux := gatedMux()
	for _, role := range roles {
		for _, rt := range routes {
			req := asRole(httptest.NewRequestWithContext(context.Background(), rt.method, rt.path, bytes.NewBufferString(rt.body)), role)
			w := httptest.NewRecorder()
			panicked := false
			func() {
				defer func() {
					if p := recover(); p != nil {
						panicked = true
						t.Errorf("%s %s as %q reached the service (panic %v); want 403", rt.method, rt.path, role, p)
					}
				}()
				mux.ServeHTTP(w, req)
			}()
			if !panicked && w.Code != http.StatusForbidden {
				t.Errorf("%s %s as %q: got %d, want 403", rt.method, rt.path, role, w.Code)
			}
		}
	}
}

var intakeRoutes = []route{
	{"GET", "/api/v1/admin/intake/folders", ""},
	{"POST", "/api/v1/admin/intake/folders", `{"label":"x","host_path":"/"}`},
	{"PATCH", "/api/v1/admin/intake/folders/f1", `{}`},
	{"DELETE", "/api/v1/admin/intake/folders/f1", ""},
	{"POST", "/api/v1/admin/intake/folders/f1/scan-now", ""},
	{"GET", "/api/v1/admin/intake/folders/f1/recent-files", ""},
}

func TestIntakeCreate_MemberForbidden(t *testing.T) {
	expectForbidden(t, intakeRoutes, "member", "editor", "viewer")
}

func TestEmailConfigs_MemberForbidden(t *testing.T) {
	expectForbidden(t, []route{
		{"GET", "/api/v1/admin/email-configs", ""},
		{"POST", "/api/v1/admin/email-configs", `{"source":"imap","label":"x","imap_host":"10.0.0.1"}`},
		{"PATCH", "/api/v1/admin/email-configs/c1", `{}`},
		{"DELETE", "/api/v1/admin/email-configs/c1", ""},
		{"POST", "/api/v1/admin/email-configs/c1/run", ""},
		{"GET", "/api/v1/admin/email-configs/c1/stats", ""},
	}, "member", "editor")
}

func TestEventStreamAdmin_MemberForbidden(t *testing.T) {
	expectForbidden(t, []route{
		{"POST", "/api/v1/admin/event-stream/tokens", `{"label":"x"}`},
		{"GET", "/api/v1/admin/event-stream/tokens", ""},
		{"DELETE", "/api/v1/admin/event-stream/tokens/t1", ""},
		{"GET", "/api/v1/admin/event-stream/tail", ""},
	}, "member", "editor")
}

func TestCapture_MemberForbidden(t *testing.T) {
	expectForbidden(t, []route{
		{"POST", "/api/v1/connectors/capture/analyze", "bytes"},
		{"POST", "/api/v1/connectors/capture/commit", `{"workspace_id":"w","folder_id":"f","page_groups":[[1]]}`},
	}, "member", "editor")
}

var webhookRoutes = []route{
	{"POST", "/api/v1/webhooks", `{"url":"https://example.com","events":["dms.document.created.v1"]}`},
	{"GET", "/api/v1/webhooks", ""},
	{"DELETE", "/api/v1/webhooks/w1", ""},
	{"GET", "/api/v1/webhooks/w1/deliveries", ""},
	{"POST", "/api/v1/webhooks/w1/rotate-secret", ""},
	{"POST", "/api/v1/webhooks/w1/test", ""},
	{"POST", "/api/v1/webhooks/w1/deliveries/d1/redeliver", ""},
}

func TestWebhooks_SessionMemberForbidden(t *testing.T) {
	expectForbidden(t, webhookRoutes, "member", "editor")
}

func TestGoogleAuthURL_MemberForbidden(t *testing.T) {
	expectForbidden(t, []route{{"GET", "/api/v1/connectors/google/auth-url", ""}}, "member", "editor")
}

// An API-key principal is gated by its webhooks:manage scope (enforced by
// SessionOrAPIKey upstream), not by a session role.
func TestWebhookGate_APIKeyWithScopeAllowed(t *testing.T) {
	r := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/webhooks", nil)
	ctx := auth.WithUser(r.Context(), auth.UserInfo{ID: uuid.New(), TenantID: uuid.New(), Role: "api_key"})
	ctx = auth.WithScopes(ctx, []string{"webhooks:manage"})
	w := httptest.NewRecorder()
	if !requireWebhookManager(w, r.WithContext(ctx)) {
		t.Fatalf("api key with webhooks:manage rejected: %d %s", w.Code, w.Body.String())
	}
	// Without the scope it is refused even if it reached the handler.
	ctx = auth.WithScopes(auth.WithUser(r.Context(), auth.UserInfo{ID: uuid.New(), TenantID: uuid.New(), Role: "api_key"}), []string{"documents:read"})
	w = httptest.NewRecorder()
	if requireWebhookManager(w, r.WithContext(ctx)) || w.Code != http.StatusForbidden {
		t.Fatalf("api key without webhooks:manage allowed (code %d)", w.Code)
	}
	for _, role := range []string{"admin", "owner"} {
		w = httptest.NewRecorder()
		if !requireWebhookManager(w, asRole(r, role)) {
			t.Fatalf("%s rejected", role)
		}
	}
}

func TestAdminGate_NoIdentityIs401(t *testing.T) {
	mux := gatedMux()
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/admin/intake/folders", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated admin route: got %d, want 401", w.Code)
	}
}

// N-11: asking for NATS creds explicitly gets 501 with an explanation
// (before any service call — svc is nil here).
func TestEventStreamToken_CredsRequest501(t *testing.T) {
	mux := gatedMux()
	req := asRole(httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/admin/event-stream/tokens",
		bytes.NewBufferString(`{"label":"x","nats_creds":true}`)), "admin")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("got %d, want 501", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("operator")) {
		t.Errorf("501 body should explain the missing NATS operator mode: %s", w.Body.String())
	}
}
