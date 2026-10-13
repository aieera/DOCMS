package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
)

type stubMux struct {
	reached bool
	tenant  string
}

func (s *stubMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.reached = true
	s.tenant = auth.TenantIDString(r)
	w.WriteHeader(http.StatusOK)
}

// A request with a valid gateway signature (the Vite host-mode proxy adds
// one) and forged identity headers but no session must get 401 and never
// reach a handler. Several notification handlers did not check identity at
// all (mark-read, unread-count, preferences) and answered with 500/200.
func TestNotificationAPI_NoSessionForgedHeaders401(t *testing.T) {
	t.Setenv("SEDOC_GATEWAY_SECRET", "gw")
	t.Setenv("SEDOC_INTERNAL_API_KEY", "ik")
	tenant, user := uuid.NewString(), uuid.NewString()
	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/notifications"},
		{http.MethodGet, "/api/v1/notifications/unread-count"},
		{http.MethodPatch, "/api/v1/notifications/" + uuid.NewString() + "/read"},
		{http.MethodPost, "/api/v1/notifications/read-all"},
		{http.MethodGet, "/api/v1/notifications/preferences"},
		{http.MethodPut, "/api/v1/notifications/preferences/matrix"},
		{http.MethodGet, "/api/v1/notifications/devices"},
		{http.MethodPost, "/api/v1/notifications/snooze"},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			stub := &stubMux{}
			r := httptest.NewRequestWithContext(context.Background(), rt.method, rt.path, bytes.NewBufferString(`{}`))
			r.Header.Set("X-Gateway-Signature", "gw")
			r.Header.Set("X-Tenant-ID", tenant)
			r.Header.Set("X-Auth-Tenant-ID", tenant)
			r.Header.Set("X-User-ID", user)
			r.Header.Set("X-User-Role", "owner")
			w := httptest.NewRecorder()
			newHTTPHandler(nil, stub).ServeHTTP(w, r)
			require.Equal(t, http.StatusUnauthorized, w.Code)
			require.False(t, stub.reached)
		})
	}

	// With the internal service key the same headers are a trusted caller.
	stub := &stubMux{}
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/notifications", nil)
	r.Header.Set("X-Gateway-Signature", "gw")
	r.Header.Set("X-Internal-Service-Key", "ik")
	r.Header.Set("X-Auth-Tenant-ID", tenant)
	r.Header.Set("X-User-ID", user)
	w := httptest.NewRecorder()
	newHTTPHandler(nil, stub).ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, tenant, stub.tenant)
}
