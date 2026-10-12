//go:build integration && prodposture
// +build integration,prodposture

// Zero-trust share public routes under the production RLS role.
//
// The recipient routes (/api/v1/zt/{token}/manifest|stream|telemetry) look
// the token up BEFORE the tenant is known, and the sender's telemetry read
// checked ownership, all on the raw pool. zt_share_tokens is FORCE RLS, so
// under the NOBYPASSRLS dms_app role every recipient link 404'd and a
// non-admin sender could never read their own telemetry. The token lookup
// now goes through the SECURITY DEFINER function from migration 000113 and
// every other read/write runs in WithTenantTx.
//
// Run:
//
//	go test -tags "integration prodposture" -run '^TestProdPosture_ZTTokenLookup$' ./internal/handler/
package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/testutil"
)

func TestProdPosture_ZTTokenLookup(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	t.Cleanup(cancel)
	db := testutil.NewProdPostureDB(ctx, t, "../../migrations")

	tenant, user, doc, ver := ztSeedTenant(ctx, t, db.Super)
	admin, public := ztMuxes(newZTTestHandler(t, db.App))

	rec, created := ztCreate(t, admin, tenant, user, "member", doc, ver, 0)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	secret := created.secret(t)

	// Anonymous recipient, twice (the second request reuses a pooled
	// connection carrying '' GUC residue).
	for i := 0; i < 2; i++ {
		rec = ztGet(public, "/api/v1/zt/"+secret+"/manifest")
		require.Equal(t, http.StatusOK, rec.Code, "manifest must resolve under dms_app: %s", rec.Body.String())
	}

	tel := httptest.NewRecorder()
	public.ServeHTTP(tel, httptest.NewRequest(http.MethodPost, "/api/v1/zt/"+secret+"/telemetry",
		bytes.NewReader([]byte(`{"event_type":"page_view","page_number":1}`))))
	require.Equal(t, http.StatusNoContent, tel.Code)

	// The non-admin sender reads their own share's telemetry.
	read := httptest.NewRecorder()
	admin.ServeHTTP(read, ztAs(httptest.NewRequest(http.MethodGet,
		"/api/v1/admin/share-links/zt/"+created.TokenID+"/telemetry", nil), tenant, user, "member"))
	require.Equal(t, http.StatusOK, read.Code, read.Body.String())
	require.Contains(t, read.Body.String(), "page_view")

	var n int
	require.NoError(t, db.Super.QueryRow(ctx,
		`SELECT count(*) FROM zt_share_telemetry WHERE tenant_id = $1`, tenant).Scan(&n))
	require.Equal(t, 1, n)
}
