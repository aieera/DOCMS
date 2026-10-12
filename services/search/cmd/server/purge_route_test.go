package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/search/internal/handler"
)

// M-13: /internal/v1/search/purge-subject deletes every indexed document a
// subject created, for the tenant named in the body. It was protected only
// by the gateway signature, which the Vite proxy adds to any request. It
// now requires the internal service key, and the body tenant must match
// the tenant the internal caller acts for.
func TestPurgeSubject_RequiresInternalKey(t *testing.T) {
	t.Setenv("SEDOC_GATEWAY_SECRET", testGatewaySecret)
	t.Setenv("SEDOC_INTERNAL_API_KEY", testInternalKey)
	tenant := uuid.NewString()
	body := `{"tenant_id":"` + tenant + `","subject_id":"` + uuid.NewString() + `"}`
	const path = "/internal/v1/search/purge-subject"

	// No internal key (gateway signature only): 401, handler never runs.
	stub := &stubMux{}
	w := httptest.NewRecorder()
	newHTTPHandler(nil, stub).ServeHTTP(w, signedRequest(http.MethodPost, path, body))
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.False(t, stub.reached)

	// Wrong internal key: 401.
	r := signedRequest(http.MethodPost, path, body)
	r.Header.Set("X-Internal-Service-Key", "nope")
	w = httptest.NewRecorder()
	newHTTPHandler(nil, stub).ServeHTTP(w, r)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.False(t, stub.reached)

	// Valid key: reaches the handler.
	r = signedRequest(http.MethodPost, path, body)
	r.Header.Set("X-Internal-Service-Key", testInternalKey)
	w = httptest.NewRecorder()
	newHTTPHandler(nil, stub).ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, stub.reached)

	// Valid key acting for another tenant than the body names: 403 from
	// the real handler, before any purge.
	mux := http.NewServeMux()
	handler.New(nil, nil, zerolog.Nop()).Register(mux)
	r = signedRequest(http.MethodPost, path, body)
	r.Header.Set("X-Internal-Service-Key", testInternalKey)
	r.Header.Set("X-Auth-Tenant-ID", uuid.NewString())
	w = httptest.NewRecorder()
	newHTTPHandler(nil, mux).ServeHTTP(w, r)
	require.Equal(t, http.StatusForbidden, w.Code)
}
