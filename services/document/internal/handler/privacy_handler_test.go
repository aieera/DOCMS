package handler

// Wave 8 Prompt 8.3 — privacy handler HTTP validation tests.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

func newPrivacyMux(t *testing.T) *http.ServeMux {
	t.Helper()
	// pool and tc may be nil — validation tests all short-circuit
	// before touching them.
	h := &PrivacyHandler{pool: nil, tc: nil, salt: "s", log: zerolog.Nop()}
	mux := http.NewServeMux()
	h.Register(mux)
	return mux
}

func TestDSR_Submit_Missing401(t *testing.T) {
	mux := newPrivacyMux(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/privacy/dsr/export",
		bytes.NewBufferString(`{"subject_email":"x@y.io"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestDSR_Submit_MissingEmail400(t *testing.T) {
	mux := newPrivacyMux(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/privacy/dsr/export",
		bytes.NewBufferString(`{}`))
	req.Header.Set("X-Auth-Tenant-ID", uuid.New().String())
	req.Header.Set("X-User-ID", uuid.New().String())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestDSR_Erase_RequiresVerificationToken400(t *testing.T) {
	mux := newPrivacyMux(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/privacy/dsr/erase",
		bytes.NewBufferString(`{"subject_email":"x@y.io"}`))
	req.Header.Set("X-Auth-Tenant-ID", uuid.New().String())
	req.Header.Set("X-User-ID", uuid.New().String())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestDSR_List_InvalidStatus400(t *testing.T) {
	mux := newPrivacyMux(t)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/privacy/dsr?status=wat", nil)
	req.Header.Set("X-Auth-Tenant-ID", uuid.New().String())
	req.Header.Set("X-User-ID", uuid.New().String())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestDSR_Get_BadUUID400(t *testing.T) {
	mux := newPrivacyMux(t)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/privacy/dsr/not-a-uuid", nil)
	req.Header.Set("X-Auth-Tenant-ID", uuid.New().String())
	req.Header.Set("X-User-ID", uuid.New().String())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}
