//go:build integration && prodposture

// M-27 — the SAML/OIDC login and SAML/OIDC JIT role mapping read the
// tenant's active sso_configs row. sso_configs is FORCE ROW LEVEL
// SECURITY, so on the raw pool under the production dms_app
// (NOBYPASSRLS) role the lookup saw zero rows and every SSO login failed
// "not configured". The lookup must run in the tenant's transaction.
package sso_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/auth/internal/sso"
	"github.com/aieera/sedoc/services/auth/internal/testenv"
)

func TestProdPosture_OIDCConfigLookup(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, db := testenv.ProdPosture(t)

	tenant := testenv.SeedTenant(ctx, t, db.Super, testenv.UniqueSlug("oidc"))
	other := testenv.SeedTenant(ctx, t, db.Super, testenv.UniqueSlug("oidc-other"))
	cfgID := uuid.Must(uuid.NewV7())
	_, err := db.Super.Exec(ctx, `
		INSERT INTO sso_configs (tenant_id, id, provider_type, display_name, config, is_active)
		VALUES ($1, $2, 'oidc', 'Okta', '{"issuer":"https://idp.example"}', true)`, tenant, cfgID)
	require.NoError(t, err)
	db.App.Reset() // fresh connections: GUC genuinely unset, as in prod

	repo := sso.NewConfigRepo()
	got, err := repo.GetActiveByTenantProvider(ctx, db.App, tenant, sso.ProviderOIDC)
	require.NoError(t, err, "active OIDC config must be found under the NOBYPASSRLS role")
	require.Equal(t, cfgID, got.ID)
	require.Equal(t, tenant, got.TenantID)

	_, err = repo.GetActiveByTenantProvider(ctx, db.App, other, sso.ProviderOIDC)
	require.Equal(t, vdmserr.KindNotFound, vdmserr.KindOf(err), "another tenant must not see this config")
}
