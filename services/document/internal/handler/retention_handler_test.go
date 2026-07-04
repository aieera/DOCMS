package handler

// Wave 10 — retention-policy handler HTTP validation tests.
// Service/DB behavior is covered in Wave 13.1 integration suite.
//
// Auth: callers() reads identity exclusively from the SessionAuth ctx
// (FIX-1 rewrite — the X-Auth-Tenant-ID/X-User-ID headers are stripped
// at the gateway and ignored by handlers), so authed requests inject
// auth.UserInfo into the request context directly.

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
)

func newRetentionMux(t *testing.T) *http.ServeMux {
	t.Helper()
	h := &RetentionPolicyHandler{pool: nil, log: zerolog.Nop()}
	mux := http.NewServeMux()
	h.Register(mux)
	return mux
}

// authedReq builds a request carrying a valid tenant+user auth context.
func authedReq(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	return req.WithContext(auth.WithUser(req.Context(), auth.UserInfo{
		TenantID: uuid.New(),
		ID:       uuid.New(),
	}))
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
	req := authedReq(http.MethodPost, "/api/v1/admin/retention-policies",
		bytes.NewBufferString(`{"retain_days":30,"then_action":"archive"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestRetentionPolicy_Create_InvalidAction400(t *testing.T) {
	mux := newRetentionMux(t)
	req := authedReq(http.MethodPost, "/api/v1/admin/retention-policies",
		bytes.NewBufferString(`{"name":"x","retain_days":30,"then_action":"bogus"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestRetentionPolicy_Create_ZeroRetainDays400(t *testing.T) {
	mux := newRetentionMux(t)
	req := authedReq(http.MethodPost, "/api/v1/admin/retention-policies",
		bytes.NewBufferString(`{"name":"x","retain_days":0,"then_action":"archive"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestRetentionPolicy_Create_BadWorkspaceUUID400(t *testing.T) {
	mux := newRetentionMux(t)
	req := authedReq(http.MethodPost, "/api/v1/admin/retention-policies",
		bytes.NewBufferString(`{"name":"x","retain_days":30,"then_action":"archive","workspace_filter":"nope"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestRetentionPolicy_Get_BadUUID400(t *testing.T) {
	mux := newRetentionMux(t)
	req := authedReq(http.MethodGet,
		"/api/v1/admin/retention-policies/not-a-uuid", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestRetentionPolicy_Update_InvalidAction400(t *testing.T) {
	mux := newRetentionMux(t)
	id := uuid.New().String()
	req := authedReq(http.MethodPatch,
		"/api/v1/admin/retention-policies/"+id,
		bytes.NewBufferString(`{"then_action":"shred"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}
