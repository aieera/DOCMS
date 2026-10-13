package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/auth/internal/scim"
)

// M-9 — Kong forwards /api/v1/scim/v2/... unstripped and the admin UI
// advertises that base path, but SCIM was only mounted at /scim/v2:
// every IdP provisioning call 404'd. Both paths must reach the SCIM
// bearer-auth chain (a request without a token gets the SCIM 401, not a
// router 404).
func TestSCIMRoute_APIv1PrefixReachesHandler(t *testing.T) {
	h := New(Config{Logger: zerolog.Nop()})
	router := h.Router(nil, nil, &SCIMWiring{
		Handler:  scim.NewHandler(scim.NewRepo(nil), "https://dms.example/api/v1/scim/v2"),
		Resolver: scim.NewTenantResolver(nil),
	}, nil, nil, nil, nil, nil, nil)

	for _, path := range []string{
		"/api/v1/scim/v2/acme/Users",
		"/scim/v2/acme/Users", // legacy mount kept
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))
		require.Equalf(t, http.StatusUnauthorized, rec.Code, "%s must reach the SCIM auth chain", path)
		require.Equalf(t, "application/scim+json", rec.Header().Get("Content-Type"), "%s: SCIM error body", path)
	}
}
