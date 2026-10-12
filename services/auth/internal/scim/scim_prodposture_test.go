//go:build integration && prodposture

// M-27 — SCIM bearer auth resolves the tenant by slug and reads the token
// hash from sso_configs; every SCIM repository call reads users/groups.
// All three tables are RLS-protected, and the code ran on the raw pool,
// so under the production dms_app (NOBYPASSRLS) role every SCIM request
// 401'd and every repo read came back empty. Resolution and the repo now
// run in the tenant's transaction.
package scim_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/auth/internal/scim"
	"github.com/aieera/sedoc/services/auth/internal/testenv"
)

func TestProdPosture_SCIMBearerAuth(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, db := testenv.ProdPosture(t)

	slug := testenv.UniqueSlug("scim")
	tenant := testenv.SeedTenant(ctx, t, db.Super, slug)
	otherSlug := testenv.UniqueSlug("scim-other")
	testenv.SeedTenant(ctx, t, db.Super, otherSlug)

	const token = "scim_prodposture_token"
	sum := sha256.Sum256([]byte(token))
	_, err := db.Super.Exec(ctx, `
		INSERT INTO sso_configs (tenant_id, id, provider_type, display_name, config, is_active)
		VALUES ($1, $2, 'saml', 'IdP', jsonb_build_object('scim_token_hash', $3::text), true)`,
		tenant, uuid.Must(uuid.NewV7()), hex.EncodeToString(sum[:]))
	require.NoError(t, err)
	testenv.SeedUser(ctx, t, db.Super, tenant, testenv.UserSpec{Email: "scim-user@example.com"})
	db.App.Reset()

	resolver := scim.NewTenantResolver(db.App)
	repo := scim.NewRepo(db.App)
	var seenTenant uuid.UUID
	var listed int
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenTenant, _ = scim.TenantFromContext(r.Context())
		users, total, err := repo.ListUsers(r.Context(), seenTenant, nil, 1, 50)
		require.NoError(t, err)
		listed = total
		require.Len(t, users, total)
		w.WriteHeader(http.StatusOK)
	})

	call := func(slug, bearer string) int {
		req := httptest.NewRequest(http.MethodGet, "/Users", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		rec := httptest.NewRecorder()
		resolver.Authenticate(slug)(next).ServeHTTP(rec, req)
		return rec.Code
	}

	require.Equal(t, http.StatusOK, call(slug, token), "a valid SCIM token must authenticate under NOBYPASSRLS")
	require.Equal(t, tenant, seenTenant)
	require.Equal(t, 1, listed, "SCIM repo reads must see the tenant's users")

	require.Equal(t, http.StatusUnauthorized, call(slug, "scim_wrong"), "wrong token")
	require.Equal(t, http.StatusUnauthorized, call(otherSlug, token), "token is bound to its tenant")

	// Writes through the repo also run in the tenant tx.
	created, err := repo.CreateUser(ctx, tenant, "provisioned@example.com", "Provisioned")
	require.NoError(t, err)
	_, err = repo.UpdateUser(ctx, tenant, created.ID, map[string]any{"display_name": "Renamed"})
	require.NoError(t, err)
	g, err := repo.CreateGroup(ctx, tenant, "Engineering", "", []uuid.UUID{created.ID})
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{created.ID}, g.MemberIDs)
	groups, n, err := repo.ListGroups(ctx, tenant, nil, 1, 10)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Len(t, groups, 1)
	require.NoError(t, repo.DeleteGroup(ctx, tenant, g.ID))
}
