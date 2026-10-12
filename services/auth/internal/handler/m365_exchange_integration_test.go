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
