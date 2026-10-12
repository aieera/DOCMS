package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
)

const (
	testGatewaySecret = "gw-secret-for-tests"
	testInternalKey   = "internal-key-for-tests"
)

// stubMux records whether a request got past the middleware and with which
// identity.
type stubMux struct {
	reached bool
	tenant  string
}

func (s *stubMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.reached = true
	s.tenant = auth.TenantIDString(r)
	w.WriteHeader(http.StatusOK)
}

func signedRequest(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	r.Header.Set("X-Gateway-Signature", testGatewaySecret)
	r.Header.Set("Content-Type", "application/json")
	return r
}

// A request with a valid gateway signature (the Vite host-mode proxy adds
// one) and forged identity headers but no session must be rejected with
// 401 on every route — never served as the named tenant/user, and never
// downgraded to a 400 by the handler.
func TestSearchAPI_NoSessionForgedHeaders401(t *testing.T) {
	t.Setenv("SEDOC_GATEWAY_SECRET", testGatewaySecret)
	t.Setenv("SEDOC_INTERNAL_API_KEY", testInternalKey)

	victimTenant, victimUser := uuid.NewString(), uuid.NewString()
	routes := []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/search", `{"query":"x"}`},
		{http.MethodGet, "/api/v1/search?q=x", ""},
		{http.MethodGet, "/api/v1/search/suggest?q=x", ""},
		{http.MethodGet, "/api/v1/saved-searches", ""},
		{http.MethodPost, "/api/v1/saved-searches", `{"name":"n","query":"q"}`},
		{http.MethodPost, "/api/v1/saved-searches/" + uuid.NewString() + "/promote", `{}`},
		{http.MethodPost, "/api/v1/platform/search/federated", `{"query":"x"}`},
		{http.MethodGet, "/api/v1/admin/permission-propagation-stats", ""},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			stub := &stubMux{}
			h := newHTTPHandler(nil, stub)
			r := signedRequest(rt.method, rt.path, rt.body)
			r.Header.Set("X-Tenant-ID", victimTenant)
			r.Header.Set("X-Auth-Tenant-ID", victimTenant)
			r.Header.Set("X-User-ID", victimUser)
			r.Header.Set("X-User-Role", "owner")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			require.Equal(t, http.StatusUnauthorized, w.Code)
			require.False(t, stub.reached, "the handler must not run without an identity")
		})
	}

	// The same headers WITH the internal key are a trusted backend caller
	// and keep working.
	stub := &stubMux{}
	r := signedRequest(http.MethodGet, "/api/v1/saved-searches", "")
	r.Header.Set("X-Internal-Service-Key", testInternalKey)
	r.Header.Set("X-Auth-Tenant-ID", victimTenant)
	r.Header.Set("X-User-ID", victimUser)
	w := httptest.NewRecorder()
	newHTTPHandler(nil, stub).ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, victimTenant, stub.tenant)
}
