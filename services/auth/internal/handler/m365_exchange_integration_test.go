//go:build integration

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/auth/internal/service"
	"github.com/aieera/sedoc/services/auth/internal/testenv"
)

// fixedEntra "verifies" every token as one Entra identity.
type fixedEntra struct{ id service.VerifiedEntraIdentity }

func (f fixedEntra) Verify(context.Context, string) (*service.VerifiedEntraIdentity, error) {
	cp := f.id
	return &cp, nil
}

type m365HTTPFixture struct {
	router http.Handler
	tenant uuid.UUID
	upn    string
}

func newM365HTTPFixture(t *testing.T, status string) m365HTTPFixture {
	t.Helper()
	env := integrationEnv(t)
	ctx := context.Background()
	upn := "addin-" + uuid.NewString()[:8] + "@corp.example"
	tenant := testenv.SeedTenant(ctx, t, env.Pool, testenv.UniqueSlug("m365http"))
	testenv.SeedUser(ctx, t, env.Pool, tenant, testenv.UserSpec{Email: upn, Status: status})
	h, _ := newIntegrationHandlerWith(t, env, func(c *service.Config) {
		c.M365Verifier = fixedEntra{id: service.VerifiedEntraIdentity{TID: "tid", OID: "oid", UPN: upn}}
	})
	return m365HTTPFixture{router: h.Router(nil, nil, nil, nil, nil, nil, nil, nil, nil), tenant: tenant, upn: upn}
}

func (f m365HTTPFixture) exchange(t *testing.T, xff string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/m365/exchange",
		strings.NewReader(`{"ms_access_token":"entra.jwt.token"}`))
	req.Header.Set("Content-Type", "application/json")
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func TestM365ExchangeHTTP_SuspendedUser403(t *testing.T) {
	f := newM365HTTPFixture(t, "suspended")
	rec := f.exchange(t, "")
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotContains(t, rec.Body.String(), "vdms_session_token")
}

// fixedGoogle "verifies" every token as one Google identity.
type fixedGoogle struct {
	id service.VerifiedGoogleIdentity
}

func (f fixedGoogle) Verify(context.Context, string) (*service.VerifiedGoogleIdentity, error) {
	cp := f.id
	return &cp, nil
}

// M-8: the Google add-on exchange answers 403 for a suspended user.
func TestGoogleExchangeHTTP_Suspended403(t *testing.T) {
	env := integrationEnv(t)
	ctx := context.Background()
	email := "gaddon-" + uuid.NewString()[:8] + "@acme.example"
	tenant := testenv.SeedTenant(ctx, t, env.Pool, testenv.UniqueSlug("ghttp"))
	testenv.SeedUser(ctx, t, env.Pool, tenant, testenv.UserSpec{Email: email, Status: "suspended"})
	h, _ := newIntegrationHandlerWith(t, env, func(c *service.Config) {
		c.GoogleVerifier = fixedGoogle{id: service.VerifiedGoogleIdentity{Sub: "s", Email: email, HD: "acme.example"}}
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/google/exchange",
		strings.NewReader(`{"google_id_token":"g.id.token"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Router(nil, nil, nil, nil, nil, nil, nil, nil, nil).ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
}

// L-3 — behind any extra proxy X-Forwarded-For is a hop list. The
// exchanges stored it raw into the INET sessions.ip_address column, so
// every exchange 500'd. They now resolve the client IP like every other
// auth handler (clientIP → the shared trusted-proxy ClientIP): here the
// peer (httptest's 192.0.2.1) and the internal hops are trusted proxies,
// so the right-most untrusted hop is the client.
func TestM365Exchange_MultiHopXFF(t *testing.T) {
	t.Setenv("SEDOC_TRUSTED_PROXIES", "192.0.2.1/32,10.0.0.0/8,172.16.0.0/12")
	f := newM365HTTPFixture(t, "active")
	rec := f.exchange(t, "198.51.100.66, 203.0.113.9, 10.1.2.3, 172.16.0.1")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var body struct {
		SessionToken string `json:"vdms_session_token"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotEmpty(t, body.SessionToken)

	var ip string
	require.NoError(t, integrationEnv(t).Pool.QueryRow(context.Background(),
		`SELECT host(ip_address) FROM sessions WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT 1`,
		f.tenant).Scan(&ip))
	require.Equal(t, "203.0.113.9", ip, "the session records the client hop, not the hop list")
}
