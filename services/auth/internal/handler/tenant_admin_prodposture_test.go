//go:build integration && prodposture

// M-27 — the owner-only tenant control plane under the production dms_app
// (NOBYPASSRLS) role:
//   - region-pin wrote `organizations.region_pin` (no such column; the
//     tenant region is primary_region) on the raw pool, and dms_app has no
//     UPDATE on organizations at all (document 000001 REVOKE). It now goes
//     through the SECURITY DEFINER org_set_region_pin (document 000111).
//   - CMK schedule/cancel updated tenant_keks (RLS) in a raw tx with no
//     tenant set → "no live KEKs" for every tenant. It now runs in the
//     tenant tx.
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
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/auth/internal/testenv"
)

func TestProdPosture_TenantRegionPinUpdate(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, db := testenv.ProdPosture(t)

	tenant := testenv.SeedTenant(ctx, t, db.Super, testenv.UniqueSlug("region"))
	owner := testenv.SeedUser(ctx, t, db.Super, tenant, testenv.UserSpec{Email: "owner@example.com", Role: "owner"})
	_, err := db.Super.Exec(ctx,
		`INSERT INTO tenant_keks (tenant_id, version, alias) VALUES ($1, 1, $2)`, tenant, "vaultdms/tenant/"+tenant.String())
	require.NoError(t, err)
	db.App.Reset()

	r := chi.NewRouter()
	NewTenantAdminHandler(db.App, zerolog.Nop()).Mount(r)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(auth.WithUser(context.Background(), auth.UserInfo{
			ID: owner, TenantID: tenant, Role: "owner",
		}))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	rec := call(http.MethodPatch, "/"+tenant.String()+"/region-pin", `{"region":"eu-west-1"}`)
	require.Equal(t, http.StatusOK, rec.Code, "region-pin under NOBYPASSRLS: %s", rec.Body.String())
	var region string
	require.NoError(t, db.Super.QueryRow(ctx, `SELECT primary_region FROM organizations WHERE id = $1`, tenant).Scan(&region))
	require.Equal(t, "eu-west-1", region)

	// Direct DML stays forbidden for the app role: only the definer writes.
	_, err = db.App.Exec(ctx, `UPDATE organizations SET primary_region = 'x' WHERE id = $1`, tenant)
	require.Error(t, err, "dms_app must not have UPDATE on organizations")

	rec = call(http.MethodPost, "/"+tenant.String()+"/schedule-cmk-deletion", `{"grace_hours":48}`)
	require.Equal(t, http.StatusOK, rec.Code, "schedule CMK deletion under NOBYPASSRLS: %s", rec.Body.String())
	rec = call(http.MethodPost, "/"+tenant.String()+"/cancel-cmk-deletion", ``)
	require.Equal(t, http.StatusNoContent, rec.Code, "cancel CMK deletion under NOBYPASSRLS: %s", rec.Body.String())

	// Cross-tenant is still refused.
	rec = call(http.MethodPatch, "/"+uuid.NewString()+"/region-pin", `{"region":"eu-west-1"}`)
	require.Equal(t, http.StatusForbidden, rec.Code)
}
