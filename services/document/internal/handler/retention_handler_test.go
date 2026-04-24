package handler

// Wave 10 — retention-policy handler HTTP validation tests.
// Service/DB behavior is covered in Wave 13.1 integration suite.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

func newRetentionMux(t *testing.T) *http.ServeMux {
	t.Helper()
	h := &RetentionPolicyHandler{pool: nil, log: zerolog.Nop()}
	mux := http.NewServeMux()
	h.Register(mux)
	return mux
}

func TestRetentionPolicy_Create_Missing401(t *testing.T) {
	mux := newRetentionMux(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/retention-policies",
		bytes.NewBufferString(`{"name":"x","retain_days":30,"then_action":"archive"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestRetentionPolicy_Create_MissingName400(t *testing.T) {
	mux := newRetentionMux(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/retention-policies",
		bytes.NewBufferString(`{"retain_days":30,"then_action":"archive"}`))
	req.Header.Set("X-Auth-Tenant-ID", uuid.New().String())
	req.Header.Set("X-User-ID", uuid.New().String())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestRetentionPolicy_Create_InvalidAction400(t *testing.T) {
	mux := newRetentionMux(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/retention-policies",
		bytes.NewBufferString(`{"name":"x","retain_days":30,"then_action":"bogus"}`))
	req.Header.Set("X-Auth-Tenant-ID", uuid.New().String())
	req.Header.Set("X-User-ID", uuid.New().String())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestRetentionPolicy_Create_ZeroRetainDays400(t *testing.T) {
	mux := newRetentionMux(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/retention-policies",
		bytes.NewBufferString(`{"name":"x","retain_days":0,"then_action":"archive"}`))
	req.Header.Set("X-Auth-Tenant-ID", uuid.New().String())
	req.Header.Set("X-User-ID", uuid.New().String())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestRetentionPolicy_Create_BadWorkspaceUUID400(t *testing.T) {
	mux := newRetentionMux(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/retention-policies",
		bytes.NewBufferString(`{"name":"x","retain_days":30,"then_action":"archive","workspace_filter":"nope"}`))
	req.Header.Set("X-Auth-Tenant-ID", uuid.New().String())
	req.Header.Set("X-User-ID", uuid.New().String())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestRetentionPolicy_Get_BadUUID400(t *testing.T) {
	mux := newRetentionMux(t)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/admin/retention-policies/not-a-uuid", nil)
	req.Header.Set("X-Auth-Tenant-ID", uuid.New().String())
	req.Header.Set("X-User-ID", uuid.New().String())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestRetentionPolicy_Update_InvalidAction400(t *testing.T) {
	mux := newRetentionMux(t)
	id := uuid.New().String()
	req := httptest.NewRequest(http.MethodPatch,
		"/api/v1/admin/retention-policies/"+id,
		bytes.NewBufferString(`{"then_action":"shred"}`))
	req.Header.Set("X-Auth-Tenant-ID", uuid.New().String())
	req.Header.Set("X-User-ID", uuid.New().String())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}
