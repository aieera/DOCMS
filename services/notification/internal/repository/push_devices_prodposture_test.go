//go:build integration && prodposture

package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/notification/internal/model"
	"github.com/aieera/sedoc/services/notification/internal/repository"
)

// M-28: push_devices (notification 000002) and notification_user_prefs
// (000003) were created without RLS "by convention", so any dms_app query
// that forgot or mistyped its tenant predicate read every tenant's push
// tokens and preferences, and the AssertRLSPosture gate (any tenant_id
// table must be ENABLE + FORCE) refuses to boot. Both tables now carry
// ENABLE + FORCE + tenant policies, and the repository reaches them only
// inside database.WithTenantTx.
func TestProdPosture_PushDevicesTenantScoped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	db := testutil.NewProdPostureDB(ctx, t, "")
	_, err := db.Super.Exec(ctx, `
		ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO dms_app;
		ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO dms_app;`)
	require.NoError(t, err)
	migrateNotificationSchema(t, db.SuperDSN)

	for _, tbl := range []string{"push_devices", "notification_user_prefs"} {
		var rls, force bool
		require.NoError(t, db.Super.QueryRow(ctx,
			`SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = $1::regclass`, tbl).Scan(&rls, &force))
		require.True(t, rls && force, "%s must be ENABLE + FORCE ROW LEVEL SECURITY", tbl)
	}

	tenantA, userA := seedTenantUser(ctx, t, db.Super)
	tenantB, userB := seedTenantUser(ctx, t, db.Super)
	repo := repository.New(db.App)

	// Push devices: the real repository works under dms_app ...
	dev := &model.PushDevice{TenantID: tenantA.String(), UserID: userA.String(), Platform: "expo", Token: "ExponentPushToken[a]"}
	require.NoError(t, repo.UpsertPushDevice(ctx, dev))
	require.NoError(t, repo.UpsertPushDevice(ctx, dev), "re-registering the same token updates in place")
	got, err := repo.ListPushDevices(ctx, tenantA.String(), userA.String())
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NoError(t, repo.UpsertPushDevice(ctx, &model.PushDevice{
		TenantID: tenantB.String(), UserID: userB.String(), Platform: "expo", Token: "ExponentPushToken[b]"}))

	// ... and a query in tenant B's context sees none of tenant A's rows,
	// even without a tenant predicate.
	var visible int
	require.NoError(t, database.WithTenantTx(ctx, db.App, tenantB, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM push_devices WHERE token = 'ExponentPushToken[a]'`).Scan(&visible)
	}))
	require.Zero(t, visible, "tenant B must not see tenant A's push tokens")
	// Writing a row for another tenant from B's context is rejected.
	err = database.WithTenantTx(ctx, db.App, tenantB, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO push_devices (tenant_id, user_id, token) VALUES ($1, $2, 'x')`, tenantA, userA)
		return e
	})
	require.ErrorContains(t, err, "row-level security")

	require.NoError(t, repo.RevokePushTokens(ctx, tenantA.String(), []string{"ExponentPushToken[a]"}))
	got, err = repo.ListPushDevices(ctx, tenantA.String(), userA.String())
	require.NoError(t, err)
	require.Empty(t, got, "a revoked token is no longer addressed")
	ok, err := repo.DeletePushDevice(ctx, tenantA.String(), userA.String(), dev.ID)
	require.NoError(t, err)
	require.True(t, ok)

	// Flat preferences: write + read back under dms_app, invisible to B.
	pref := &model.UserPreference{TenantID: tenantA.String(), UserID: userA.String(),
		EmailEnabled: false, PushEnabled: true, QuietHoursFrom: 22, QuietHoursTo: 7}
	require.NoError(t, repo.UpsertPreference(ctx, pref))
	back, err := repo.GetPreference(ctx, tenantA.String(), userA.String())
	require.NoError(t, err)
	require.False(t, back.EmailEnabled)
	require.Equal(t, 22, back.QuietHoursFrom)
	require.Equal(t, 7, back.QuietHoursTo)
	require.NoError(t, database.WithTenantTx(ctx, db.App, tenantB, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM notification_user_prefs`).Scan(&visible)
	}))
	require.Zero(t, visible, "tenant B must not see tenant A's preferences")
}
