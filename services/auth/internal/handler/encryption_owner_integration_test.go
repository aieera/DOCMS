//go:build integration

// Client-only gate (addendum W list): the web UI treats KEK rotate/revoke
// as owner-only, but the server let any admin rotate the tenant KEK or
// revoke a version (break-glass: data wrapped under it becomes
// unreadable). Rotate and revoke now require the owner role.
package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/auth/internal/testenv"
)

func TestEncryptionRotate_AdminForbidden(t *testing.T) {
	env := integrationEnv(t)
	ctx := context.Background()
	tenant := testenv.SeedTenant(ctx, t, env.Pool, testenv.UniqueSlug("kek"))
	admin := testenv.SeedUser(ctx, t, env.Pool, tenant, testenv.UserSpec{Email: "kek-admin-" + uuid.NewString()[:8] + "@example.com", Role: "admin"})
	owner := testenv.SeedUser(ctx, t, env.Pool, tenant, testenv.UserSpec{Email: "kek-owner-" + uuid.NewString()[:8] + "@example.com", Role: "owner"})
	_, err := env.Pool.Exec(ctx,
		`INSERT INTO tenant_keks (tenant_id, version, alias) VALUES ($1, 1, $2)`, tenant, "vaultdms/tenant/"+tenant.String())
	require.NoError(t, err)

	r := chi.NewRouter()
	NewEncryptionAdminHandler(env.Pool, zerolog.Nop()).Mount(r)
	call := func(user uuid.UUID, role, path, body string) int {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req = req.WithContext(auth.WithUser(ctx, auth.UserInfo{ID: user, TenantID: tenant, Role: role}))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}

	require.Equal(t, http.StatusForbidden, call(admin, "admin", "/rotate", `{}`), "an admin must not rotate the tenant KEK")
	require.Equal(t, http.StatusForbidden, call(admin, "admin", "/revoke", `{"version":1}`), "an admin must not revoke a KEK version")

	require.Equal(t, http.StatusOK, call(owner, "owner", "/rotate", `{}`), "the owner rotates")
	require.Equal(t, http.StatusOK, call(owner, "owner", "/revoke", `{"version":1}`), "the owner revokes")
}
