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

func TestRequireAPIKey_BypassedWhenKeyEmpty(t *testing.T) {
	// In dev mode the operator may leave InternalAPIKey unset; the gate
	// then acts as a no-op so /internal routes remain reachable. This
	// behavior is documented — if it changes, this test catches it.
	h := &Handler{apiKey: ""}
	called := false
	inner := func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	w := httptest.NewRecorder()
	h.requireAPIKey(inner)(w, req)

	if !called {
		t.Error("inner handler must run when apiKey is empty (dev mode)")
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
