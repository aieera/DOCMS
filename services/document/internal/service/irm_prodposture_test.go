//go:build integration && prodposture
// +build integration,prodposture

// IRM license-check callback under the production RLS role.
//
// CheckLicense / OpenContent resolve a license by token hash BEFORE the
// tenant is known (the recipient may be external, with no session). The
// lookup used the raw pool and only worked because migration 000089's
// policy was fail-open for an empty tenant context. With the strict
// policy from migration 000109 the lookup goes through the SECURITY
// DEFINER function irm_lookup_license_by_token_hash; every later read and
// write runs in WithTenantTx.
//
// Run:
//
//	go test -tags "integration prodposture" -run '^TestProdPosture_IRMLicenseLookup$' ./internal/service/
package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"
)

func TestProdPosture_IRMLicenseLookup(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	t.Cleanup(cancel)

	db := testutil.NewProdPostureDB(ctx, t, "../../migrations")
	f := seedIRMLicense(ctx, t, db.Super)
	other := seedIRMLicense(ctx, t, db.Super) // a second tenant's license

	repos := repository.New(db.App)
	docSvc := service.New(db.App, repos, stubPolicy{allow: true}, zerolog.Nop())
	irm := service.NewIRMService(db.App, repos, nil, nil, docSvc, zerolog.Nop())

	// Anonymous recipient: no tenant, no user on ctx.
	anon := context.Background()
	reason, res, err := irm.CheckLicense(anon, f.container, f.token, [16]byte{}, "iphash", "ua")
	require.NoError(t, err)
	require.Equal(t, model.LicenseOK, reason, "license must resolve under dms_app NOBYPASSRLS")
	require.NotNil(t, res)
	require.Equal(t, "Sealed", res.Title)

	// The open was counted + logged inside the owning tenant's tx.
	var opens, events int
	require.NoError(t, db.Super.QueryRow(ctx,
		`SELECT open_count FROM irm_licenses WHERE tenant_id = $1 AND id = $2`,
		f.tenant, f.license).Scan(&opens))
	require.Equal(t, 1, opens)
	require.NoError(t, db.Super.QueryRow(ctx,
		`SELECT count(*) FROM irm_license_events WHERE tenant_id = $1 AND license_id = $2 AND event_type = 'opened'`,
		f.tenant, f.license).Scan(&events))
	require.Equal(t, 1, events)

	// Token/container confusion: tenant B's token against tenant A's
	// container id is not_found, never a cross-tenant hit.
	reason, _, err = irm.CheckLicense(anon, f.container, other.token, [16]byte{}, "", "")
	require.NoError(t, err)
	require.Equal(t, model.LicenseDenyReason("not_found"), reason)

	// Unknown token.
	reason, _, err = irm.CheckLicense(anon, f.container, "nope", [16]byte{}, "", "")
	require.NoError(t, err)
	require.Equal(t, model.LicenseDenyReason("not_found"), reason)

	// Revocation is honoured through the same path.
	_, err = db.Super.Exec(ctx,
		`UPDATE irm_licenses SET revoked_at = now() WHERE tenant_id = $1 AND id = $2`, f.tenant, f.license)
	require.NoError(t, err)
	reason, _, err = irm.CheckLicense(anon, f.container, f.token, [16]byte{}, "", "")
	require.NoError(t, err)
	require.Equal(t, model.LicenseRevoked, reason)
}
