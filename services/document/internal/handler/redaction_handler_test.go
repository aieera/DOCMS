package handler

// Wave 11.5 — redaction endpoint validation tests.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/stretchr/testify/require"
)

func newRedactionMux(t *testing.T) *http.ServeMux {
	t.Helper()
	h := &RedactionHandler{pool: nil, holds: nil, log: zerolog.Nop()}
	mux := http.NewServeMux()
	h.Register(mux)
	return mux
}

func TestRedact_Missing401(t *testing.T) {
	mux := newRedactionMux(t)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/documents/"+uuid.New().String()+"/redact",
		bytes.NewBufferString(`{"reason":"x","regions":[{"page":1}]}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestRedact_Forbidden403_NonCompliance(t *testing.T) {
	mux := newRedactionMux(t)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/documents/"+uuid.New().String()+"/redact",
		bytes.NewBufferString(`{"reason":"x","regions":[{"page":1}]}`))
	req = req.WithContext(auth.WithUser(req.Context(), auth.UserInfo{TenantID: uuid.New(), ID: uuid.New(), Role: "member"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)
}

func TestRedact_BadDocUUID400(t *testing.T) {
	mux := newRedactionMux(t)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/documents/not-a-uuid/redact",
		bytes.NewBufferString(`{"reason":"x","regions":[{"page":1}]}`))
	req = req.WithContext(auth.WithUser(req.Context(), auth.UserInfo{TenantID: uuid.New(), ID: uuid.New(), Role: "compliance_officer"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestRedact_MissingReason400(t *testing.T) {
	mux := newRedactionMux(t)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/documents/"+uuid.New().String()+"/redact",
		bytes.NewBufferString(`{"regions":[{"page":1}]}`))
	req = req.WithContext(auth.WithUser(req.Context(), auth.UserInfo{TenantID: uuid.New(), ID: uuid.New(), Role: "compliance_officer"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestRedact_EmptyRegionsAndEntities400(t *testing.T) {
	mux := newRedactionMux(t)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/documents/"+uuid.New().String()+"/redact",
		bytes.NewBufferString(`{"reason":"x"}`))
	req = req.WithContext(auth.WithUser(req.Context(), auth.UserInfo{TenantID: uuid.New(), ID: uuid.New(), Role: "compliance_officer"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestRedact_BadVersionID400(t *testing.T) {
	mux := newRedactionMux(t)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/documents/"+uuid.New().String()+"/redact",
		bytes.NewBufferString(`{"reason":"x","version_id":"bad","regions":[{"page":1}]}`))
	req = req.WithContext(auth.WithUser(req.Context(), auth.UserInfo{TenantID: uuid.New(), ID: uuid.New(), Role: "compliance_officer"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}
