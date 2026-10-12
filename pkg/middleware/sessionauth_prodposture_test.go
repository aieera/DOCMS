//go:build integration && prodposture
// +build integration,prodposture

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
)

// TestProdPosture_SessionAuthResolvesUser (C-3): on the NOBYPASSRLS pool a
// valid session resolves to the user's tenant, id, email and role through
// auth_session_identity, and the group memberships load inside the tenant
// transaction (group_members is FORCE RLS too).
func TestProdPosture_SessionAuthResolvesUser(t *testing.T) {
	f := newIdentityFixture(t)
	user := f.seedUser(t, "alice@example.com", "admin", "active")
	group := mustSeedGroup(t, f, user)
	token := f.seedSession(t, user)

	for name, mw := range map[string]func(http.Handler) http.Handler{
		"SessionAuth":         SessionAuth(SessionAuthConfig{Pool: f.db.App}),
		"SessionAuthOptional": SessionAuthOptional(SessionAuthConfig{Pool: f.db.App}),
		"SessionOrAPIKey":     SessionOrAPIKey(SessionAuthConfig{Pool: f.db.App}, "documents:read"),
	} {
		t.Run(name, func(t *testing.T) {
			code, seen := capture(mw, sessionReq(token))
			require.Equal(t, http.StatusOK, code)
			require.NotNil(t, seen, "identity must be bound")
			require.Equal(t, f.tenant, seen.TenantID)
			require.Equal(t, user, seen.ID)
			require.Equal(t, "alice@example.com", seen.Email)
			require.Equal(t, "admin", seen.Role)
			require.Contains(t, seen.Groups, group, "group memberships must load under the tenant tx")
		})
	}

	t.Run("unknown session is 401, not 503", func(t *testing.T) {
		code, _ := capture(SessionAuth(SessionAuthConfig{Pool: f.db.App}), sessionReq("no-such-session"))
		require.Equal(t, http.StatusUnauthorized, code)
	})
}

// TestProdPosture_APIKeyAuthResolvesKey (C-3): on the NOBYPASSRLS pool an
// API key resolves through auth_lookup_api_key_by_hash; revoked, expired
// and unknown keys are 401, a missing scope is 403.
func TestProdPosture_APIKeyAuthResolvesKey(t *testing.T) {
	f := newIdentityFixture(t)
	user := f.seedUser(t, "erp@example.com", "member", "active")
	good, _ := f.seedAPIKey(t, user, []string{"documents:read", "integrations:*"}, false, false)
	revoked, _ := f.seedAPIKey(t, user, []string{"documents:read"}, true, false)
	expired, _ := f.seedAPIKey(t, user, []string{"documents:read"}, false, true)

	keyReq := func(token string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		return r
	}
	mw := APIKeyAuth(APIKeyAuthConfig{Pool: f.db.App, RequiredScope: "documents:read"})

	code, seen := capture(mw, keyReq(good))
	require.Equal(t, http.StatusOK, code)
	require.NotNil(t, seen)
	require.Equal(t, f.tenant, seen.TenantID)
	require.Equal(t, user, seen.ID)
	require.Equal(t, "api_key", seen.Role)

	var scopes []string
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { scopes = auth.GetScopes(r.Context()) }))
	h.ServeHTTP(httptest.NewRecorder(), keyReq(good))
	require.ElementsMatch(t, []string{"documents:read", "integrations:*"}, scopes)

	for name, tok := range map[string]string{"revoked": revoked, "expired": expired, "unknown": "vdms_unknown"} {
		code, _ := capture(mw, keyReq(tok))
		require.Equalf(t, http.StatusUnauthorized, code, "%s key", name)
	}
	code, _ = capture(APIKeyAuth(APIKeyAuthConfig{Pool: f.db.App, RequiredScope: "mcp:read"}), keyReq(good))
	require.Equal(t, http.StatusForbidden, code, "missing scope")

	// The same key through SessionOrAPIKey's dispatch.
	code, seen = capture(SessionOrAPIKey(SessionAuthConfig{Pool: f.db.App}, "documents:read"), keyReq(good))
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, f.tenant, seen.TenantID)
}
