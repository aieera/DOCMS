//go:build integration

// DEF-09 — self-registration is gated by SEDOC_SELF_REGISTRATION_ENABLED
// (default off). Off: 403 and no user row. On: the pre-existing behavior
// (201, an active member in the named tenant).
package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/auth/internal/testenv"
)

func registerOnce(t *testing.T, selfRegistration bool) (code int, tenant uuid.UUID, email string) {
	t.Helper()
	env := integrationEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	_, svc := newIntegrationHandler(t, env)
	h := New(Config{Service: svc, Logger: zerolog.Nop(), SelfRegistrationEnabled: selfRegistration})
	router := h.Router(nil, nil, nil, nil, nil, nil, nil, nil, nil)

	slug := testenv.UniqueSlug("selfreg")
	tenant = testenv.SeedTenant(ctx, t, env.Pool, slug)
	email = "selfreg-" + uuid.NewString()[:8] + "@example.com"
	body := `{"email":"` + email + `","password":"Correct-Horse-9!","display_name":"Self Reg","tenant_slug":"` + slug + `"}`
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/auth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec.Code, tenant, email
}

func countUsers(t *testing.T, tenant uuid.UUID, email string) (n int, role, status string) {
	t.Helper()
	env := integrationEnv(t)
	require.NoError(t, env.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM users WHERE tenant_id = $1 AND email = $2`, tenant, email).Scan(&n))
	if n > 0 {
		require.NoError(t, env.Pool.QueryRow(context.Background(),
			`SELECT role, status FROM users WHERE tenant_id = $1 AND email = $2`, tenant, email).Scan(&role, &status))
	}
	return n, role, status
}

func TestRegister_FlagOff_403AndNoUser(t *testing.T) {
	code, tenant, email := registerOnce(t, false)
	require.Equal(t, http.StatusForbidden, code)
	n, _, _ := countUsers(t, tenant, email)
	require.Zero(t, n, "a disabled self-registration must not create a user")
}

func TestRegister_FlagOn_CreatesActiveMember(t *testing.T) {
	code, tenant, email := registerOnce(t, true)
	require.Equal(t, http.StatusCreated, code)
	n, role, status := countUsers(t, tenant, email)
	require.Equal(t, 1, n)
	require.Equal(t, "member", role)
	require.Equal(t, "active", status)
}
