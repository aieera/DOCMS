package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/search/internal/handler"
	"github.com/aieera/sedoc/services/search/internal/opensearch"
	"github.com/aieera/sedoc/services/search/internal/service"
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

// M-13 follow-up: the tenant check failed open — an internal-key holder
// could purge ANY tenant by simply omitting X-Auth-Tenant-ID. The caller
// must name the tenant it acts for and it must equal the body tenant.
func TestPurgeSubject_NoTenantHeader403(t *testing.T) {
	t.Setenv("SEDOC_GATEWAY_SECRET", testGatewaySecret)
	t.Setenv("SEDOC_INTERNAL_API_KEY", testInternalKey)

	// A fake OpenSearch: accepts the template PUT and records purges.
	var purged []string
	osSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "_delete_by_query") {
			purged = append(purged, r.URL.Path)
			_, _ = w.Write([]byte(`{"deleted":3}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(osSrv.Close)
	osCli, err := opensearch.NewReal(context.Background(), opensearch.Config{URL: osSrv.URL, Logger: zerolog.Nop()})
	require.NoError(t, err)
	mux := http.NewServeMux()
	handler.New(service.New(service.Config{OS: osCli, Logger: zerolog.Nop()}), nil, zerolog.Nop()).Register(mux)
	h := newHTTPHandler(nil, mux)

	tenant := uuid.NewString()
	body := `{"tenant_id":"` + tenant + `","subject_id":"` + uuid.NewString() + `"}`
	send := func(actingTenant string) int {
		r := signedRequest(http.MethodPost, "/internal/v1/search/purge-subject", body)
		r.Header.Set("X-Internal-Service-Key", testInternalKey)
		if actingTenant != "" {
			r.Header.Set("X-Auth-Tenant-ID", actingTenant)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	require.Equal(t, http.StatusForbidden, send(""), "no acting tenant must be refused")
	require.Equal(t, http.StatusForbidden, send(uuid.NewString()), "a different acting tenant must be refused")
	require.Empty(t, purged, "nothing may be purged on a refused call")
	require.Equal(t, http.StatusOK, send(tenant))
	require.Len(t, purged, 1)
	require.Contains(t, purged[0], tenant)
}
