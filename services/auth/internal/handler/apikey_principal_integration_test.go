//go:build integration

// DEC-14 / H-5 — an API key is a programmatic credential, not a user
// session. On the auth service it may read GET /api/v1/auth/me and
// nothing else: before the fix a key with only documents:read could
// register a passkey on its owner's account (then log in passwordless
// after the key was revoked), reset MFA and revoke sessions.
package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/aieera/sedoc/services/auth/internal/service"
	"github.com/aieera/sedoc/services/auth/internal/testenv"
)

const principalTestPassword = "Correct-Horse-9!"

type principalFixture struct {
	router     http.Handler
	apiKey     string
	sessionTok string
}

func newPrincipalFixture(t *testing.T) principalFixture {
	t.Helper()
	env := integrationEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	h, svc := newIntegrationHandler(t, env)
	slug := testenv.UniqueSlug("apikey")
	tenant := testenv.SeedTenant(ctx, t, env.Pool, slug)
	hash, err := bcrypt.GenerateFromPassword([]byte(principalTestPassword), bcrypt.MinCost)
	require.NoError(t, err)
	email := "owner-" + uuid.NewString()[:8] + "@example.com"
	user := testenv.SeedUser(ctx, t, env.Pool, tenant, testenv.UserSpec{
		Email: email, Role: "owner", PasswordHash: string(hash),
	})

	issued, err := svc.IssueAPIKey(ctx, service.IssueAPIKeyInput{
		TenantID: tenant, UserID: user, Name: "docs-reader", Scopes: []string{"documents:read"},
	})
	require.NoError(t, err)

	res, err := svc.Login(ctx, service.LoginInput{
		Email: email, Password: principalTestPassword, TenantSlug: slug,
		IPAddress: "203.0.113.7", UserAgent: "test",
	})
	require.NoError(t, err)
	require.NotNil(t, res.Session, "password login must yield a session")

	return principalFixture{
		router:     h.Router(nil, nil, nil, nil, nil, nil, nil, nil, nil),
		apiKey:     issued.Key,
		sessionTok: res.Session.Token,
	}
}

func (f principalFixture) do(t *testing.T, method, path, bearer, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func TestAPIKeyPrincipal_403OnWebAuthnRegistration(t *testing.T) {
	f := newPrincipalFixture(t)
	rec := f.do(t, http.MethodPost, "/api/v1/auth/webauthn/registration/begin", f.apiKey, `{}`)
	require.Equal(t, http.StatusForbidden, rec.Code,
		"an API key must not register a passkey on its owner's account: %s", rec.Body.String())
}

func TestAPIKeyPrincipal_403OnMFASetup(t *testing.T) {
	f := newPrincipalFixture(t)
	rec := f.do(t, http.MethodPost, "/api/v1/auth/mfa/setup", f.apiKey, `{}`)
	require.Equal(t, http.StatusForbidden, rec.Code,
		"an API key must not (re)provision its owner's TOTP secret: %s", rec.Body.String())
}

func TestAPIKeyPrincipal_200OnMe(t *testing.T) {
	f := newPrincipalFixture(t)
	rec := f.do(t, http.MethodGet, "/api/v1/auth/me", f.apiKey, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// Every other credential-management and account route refuses the key;
// the same routes keep working for a real session.
func TestAPIKeyPrincipal_403OnEveryOtherAuthRoute(t *testing.T) {
	f := newPrincipalFixture(t)
	routes := []struct{ method, path, body string }{
		{http.MethodPatch, "/api/v1/auth/me/locale", `{"locale":"en"}`},
		{http.MethodPost, "/api/v1/auth/logout", ``},
		{http.MethodGet, "/api/v1/auth/users/directory", ``},
		{http.MethodGet, "/api/v1/auth/sessions/", ``},
		{http.MethodPost, "/api/v1/auth/sessions/revoke-all", ``},
		{http.MethodDelete, "/api/v1/auth/sessions/" + uuid.NewString(), ``},
		{http.MethodPost, "/api/v1/auth/mfa/confirm", `{"totp_code":"000000"}`},
		{http.MethodPost, "/api/v1/auth/mfa/disable", `{"totp_code":"000000"}`},
		{http.MethodGet, "/api/v1/auth/mfa/methods", ``},
		{http.MethodPost, "/api/v1/auth/mfa/email/enroll", `{"email":"x@example.com"}`},
		{http.MethodPost, "/api/v1/auth/mfa/sms/enroll", `{"phone":"+14155552671"}`},
		{http.MethodPost, "/api/v1/auth/mfa/email/enroll/verify", `{"code":"123456"}`},
		{http.MethodPost, "/api/v1/auth/mfa/sms/enroll/verify", `{"code":"123456"}`},
		{http.MethodDelete, "/api/v1/auth/mfa/methods/totp", ``},
		{http.MethodGet, "/api/v1/auth/api-keys/", ``},
		{http.MethodPost, "/api/v1/auth/api-keys/", `{"name":"x","scopes":["documents:read"]}`},
		{http.MethodDelete, "/api/v1/auth/api-keys/" + uuid.NewString(), ``},
		{http.MethodPost, "/api/v1/auth/webauthn/registration/finish", `{}`},
		{http.MethodGet, "/api/v1/auth/webauthn/credentials", ``},
		{http.MethodDelete, "/api/v1/auth/webauthn/credentials/abc", ``},
	}
	for _, rt := range routes {
		rec := f.do(t, rt.method, rt.path, f.apiKey, rt.body)
		require.Equalf(t, http.StatusForbidden, rec.Code, "%s %s with an API key: %s", rt.method, rt.path, rec.Body.String())
	}

	// A session principal is unaffected.
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/auth/me"},
		{http.MethodGet, "/api/v1/auth/sessions/"},
		{http.MethodGet, "/api/v1/auth/api-keys/"},
		{http.MethodGet, "/api/v1/auth/mfa/methods"},
	} {
		rec := f.do(t, rt.method, rt.path, f.sessionTok, "")
		require.Equalf(t, http.StatusOK, rec.Code, "%s %s with a session: %s", rt.method, rt.path, rec.Body.String())
	}
}
