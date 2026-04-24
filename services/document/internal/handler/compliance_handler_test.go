package handler

// Wave 8 Prompt 8.2 — compliance handler request-validation tests.
//
// DB-level behavior (the outbox insert + tx rollback semantics, hold
// bindings) is exercised in the compliance integration suite
// (Wave 13.1). Here we pin the HTTP-level invariants:
//   - 401 when the caller headers are missing
//   - 400 on malformed body / invalid UUIDs / unsupported status filter
//   - 423 Locked when the service returns ErrLegalHold (via errors pkg)

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
)

func newHoldsMux(t *testing.T) *http.ServeMux {
	t.Helper()
	// Passing nil svc is OK: validation errors (which we test) all
	// short-circuit before service dispatch. Any test that would
	// reach the service is explicitly flagged below.
	h := &HoldsHandler{svc: nil, log: zerolog.Nop()}
	mux := http.NewServeMux()
	h.Register(mux)
	return mux
}

func TestHolds_Create_Missing401(t *testing.T) {
	mux := newHoldsMux(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/compliance/holds",
		bytes.NewBufferString(`{"name":"x"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestHolds_Create_MalformedJSON400(t *testing.T) {
	mux := newHoldsMux(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/compliance/holds",
		bytes.NewBufferString("{{{"))
	req.Header.Set("X-Auth-Tenant-ID", uuid.New().String())
	req.Header.Set("X-User-ID", uuid.New().String())
	req.Header.Set("X-User-Role", "compliance_officer")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestHolds_Create_InvalidDocUUID400(t *testing.T) {
	mux := newHoldsMux(t)
	body := `{"name":"Matter A","document_ids":["not-a-uuid"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/compliance/holds",
		bytes.NewBufferString(body))
	req.Header.Set("X-Auth-Tenant-ID", uuid.New().String())
	req.Header.Set("X-User-ID", uuid.New().String())
	req.Header.Set("X-User-Role", "compliance_officer")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestHolds_List_InvalidStatus400(t *testing.T) {
	mux := newHoldsMux(t)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/compliance/holds?status=bogus", nil)
	req.Header.Set("X-Auth-Tenant-ID", uuid.New().String())
	req.Header.Set("X-User-ID", uuid.New().String())
	req.Header.Set("X-User-Role", "compliance_officer")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestHolds_Release_MissingApprover400(t *testing.T) {
	mux := newHoldsMux(t)
	id := uuid.New().String()
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/compliance/holds/"+id+"/release",
		bytes.NewBufferString(`{"reason":"done"}`))
	req.Header.Set("X-Auth-Tenant-ID", uuid.New().String())
	req.Header.Set("X-User-ID", uuid.New().String())
	req.Header.Set("X-User-Role", "compliance_officer")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestHolds_Get_BadUUID400(t *testing.T) {
	mux := newHoldsMux(t)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/compliance/holds/not-a-uuid", nil)
	req.Header.Set("X-Auth-Tenant-ID", uuid.New().String())
	req.Header.Set("X-User-ID", uuid.New().String())
	req.Header.Set("X-User-Role", "compliance_officer")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

// writeErr should convert ErrLegalHold to HTTP 423. Exercises the
// pkg/errors mapping change we made in this prompt.
func TestHolds_Create_Forbidden403_WhenNotComplianceOfficer(t *testing.T) {
	// Wave 11.2: member role cannot create holds even with valid body.
	mux := newHoldsMux(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/compliance/holds",
		bytes.NewBufferString(`{"name":"x","document_ids":["`+uuid.New().String()+`"]}`))
	req.Header.Set("X-Auth-Tenant-ID", uuid.New().String())
	req.Header.Set("X-User-ID", uuid.New().String())
	req.Header.Set("X-User-Role", "member")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}
}

func TestHolds_LegalHoldMapsTo423(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	writeErr(w, r, vdmserr.ErrLegalHold)
	if w.Code != http.StatusLocked {
		t.Fatalf("want 423, got %d", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["type"] != "LEGAL_HOLD" {
		t.Errorf("body type: %v", body["type"])
	}
}
