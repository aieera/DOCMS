package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Billing's /internal/v1/* routes are protected by X-API-Key. We test that
// the auth gate runs before any business logic.

func TestRequireAPIKey_RejectsMissingKey(t *testing.T) {
	h := &Handler{apiKey: "supersecret"}
	called := false
	inner := func(w http.ResponseWriter, r *http.Request) { called = true }

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	w := httptest.NewRecorder()
	h.requireAPIKey(inner)(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
	if called {
		t.Error("inner handler ran without valid key")
	}
}

func TestRequireAPIKey_RejectsWrongKey(t *testing.T) {
	h := &Handler{apiKey: "supersecret"}
	called := false
	inner := func(w http.ResponseWriter, r *http.Request) { called = true }

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-API-Key", "wrong")
	w := httptest.NewRecorder()
	h.requireAPIKey(inner)(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
	if called {
		t.Error("inner handler ran with wrong key")
	}
}

func TestRequireAPIKey_AdmitsCorrectKey(t *testing.T) {
	h := &Handler{apiKey: "supersecret"}
	called := false
	inner := func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-API-Key", "supersecret")
	w := httptest.NewRecorder()
	h.requireAPIKey(inner)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	if !called {
		t.Error("inner handler did not run with correct key")
	}
}

// With no InternalAPIKey configured, every key-gated internal route must
// fail closed (503) whatever the caller sends — never fall open. With the
// org-mutation definers, an open /features route would be an arbitrary-org
// write path on a misconfigured deployment.
func TestInternalRoutes_FailClosedWithoutKey(t *testing.T) {
	h := &Handler{apiKey: ""} // svc nil: reaching a handler would panic
	mux := http.NewServeMux()
	h.Register(mux)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/internal/v1/tenants/provision", `{"org_name":"x","admin_email":"a@b.c"}`},
		{http.MethodGet, "/internal/v1/tenants/t1/subscription", ""},
		{http.MethodGet, "/internal/v1/tenants/t1/features", ""},
		{http.MethodPut, "/internal/v1/tenants/t1/features", `{"ai_enabled":true}`},
		{http.MethodGet, "/internal/v1/plans", ""},
	} {
		for _, key := range []string{"", "anything"} {
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
			if key != "" {
				req.Header.Set("X-API-Key", key)
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("%s %s (key %q) with no InternalAPIKey: status %d, want 503", tc.method, tc.path, key, w.Code)
			}
		}
	}
}

func TestProvision_RejectsInvalidJSON(t *testing.T) {
	mux := http.NewServeMux()
	h := &Handler{apiKey: ""} // svc nil — validation short-circuits
	mux.HandleFunc("POST /internal/v1/tenants/provision", h.provision)

	req := httptest.NewRequest(http.MethodPost, "/internal/v1/tenants/provision", bytes.NewBufferString("{{{"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestProvision_RequiresOrgAndEmail(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"missing both", `{}`},
		{"missing email", `{"org_name":"Acme"}`},
		{"missing org", `{"admin_email":"a@a.com"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			h := &Handler{apiKey: ""}
			mux.HandleFunc("POST /internal/v1/tenants/provision", h.provision)

			req := httptest.NewRequest(http.MethodPost, "/internal/v1/tenants/provision", bytes.NewBufferString(tc.body))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d (body: %s)", w.Code, w.Body.String())
			}
		})
	}
}

func TestListPlans_NoAuthNeededInDevMode(t *testing.T) {
	mux := http.NewServeMux()
	h := &Handler{apiKey: ""}
	mux.HandleFunc("GET /internal/v1/plans", h.listPlans)

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/plans", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("plans response not JSON: %v", err)
	}
	if _, ok := got["standard"]; !ok {
		t.Error("expected 'standard' plan in response")
	}
}

func TestUpdateFeatures_RejectsInvalidJSON(t *testing.T) {
	mux := http.NewServeMux()
	h := &Handler{apiKey: ""}
	mux.HandleFunc("PUT /internal/v1/tenants/{tenantId}/features", h.updateFeatures)

	req := httptest.NewRequest(http.MethodPut, "/internal/v1/tenants/t1/features", bytes.NewBufferString("not json"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}
