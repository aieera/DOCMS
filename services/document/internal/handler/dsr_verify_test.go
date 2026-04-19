package handler

// Wave 11.4 — DSR verify endpoint HTTP validation tests.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func newDSRVerifyMux(t *testing.T) *http.ServeMux {
	t.Helper()
	// Pool + rdb nil — all handled paths short-circuit before them.
	h := &DSRVerifyHandler{pool: nil, rdb: nil, log: zerolog.Nop()}
	mux := http.NewServeMux()
	h.Register(mux)
	return mux
}

func TestDSRVerify_Missing401(t *testing.T) {
	mux := newDSRVerifyMux(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/privacy/verify/request-token",
		bytes.NewBufferString(`{"subject_email":"a@b"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestDSRVerify_MissingEmail400(t *testing.T) {
	mux := newDSRVerifyMux(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/privacy/verify/request-token",
		bytes.NewBufferString(`{}`))
	req.Header.Set("X-Tenant-ID", uuid.New().String())
	req.Header.Set("X-User-ID", uuid.New().String())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDSRVerify_MalformedEmail400(t *testing.T) {
	mux := newDSRVerifyMux(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/privacy/verify/request-token",
		bytes.NewBufferString(`{"subject_email":"no-at-sign"}`))
	req.Header.Set("X-Tenant-ID", uuid.New().String())
	req.Header.Set("X-User-ID", uuid.New().String())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHashDSRToken_Deterministic(t *testing.T) {
	// Guard that the hash function stays stable — the activity and
	// handler MUST produce the same digest for the same plaintext.
	require.Equal(t,
		HashDSRToken("hello"),
		HashDSRToken("hello"),
		"HashDSRToken must be deterministic")
	require.NotEqual(t,
		HashDSRToken("hello"),
		HashDSRToken("world"),
	)
}

func TestDSRTokenRedisKey_Canonical(t *testing.T) {
	// Lowercases + trims email so variations don't spawn parallel
	// tokens. The workflow activity must use identical rules.
	tenant := uuid.MustParse("0191d38a-68a0-7fff-8000-000000000001")
	k1 := DSRTokenRedisKey(tenant, "  ALICE@example.com  ")
	k2 := DSRTokenRedisKey(tenant, "alice@example.com")
	require.Equal(t, k1, k2)
}
