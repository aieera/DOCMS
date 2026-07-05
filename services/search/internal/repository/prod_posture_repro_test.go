//go:build integration && prodposture
// +build integration,prodposture

// Prod-posture repro: search saved-search repository ignores tenant context.
//
// BUG (audit-verified, STATE_OF_THE_PROJECT 2026-07-03): this package's
// Repository (repository.go ~line 31 and saved_search_alerts.go) runs every
// query on the raw *pgxpool.Pool without ever establishing
// app.current_tenant — no database.WithTenantTx / WithTenant wrapper. Dev
// and the default integration lane connect as a BYPASSRLS superuser, which
// masks the bug. In production the pool connects as dms_app (NOBYPASSRLS)
// and saved_searches / saved_search_subscribers carry ENABLE + FORCE ROW
// LEVEL SECURITY with a tenant policy on current_setting('app.current_tenant'),
// so every read fails CLOSED: saved searches and alert subscribers come
// back as 0 rows / not-found even though the rows exist.
//
// Tracks: https://github.com/aieera/DOCMS/issues/70
//
// This test seeds data the tenant-correct way (WithTenantTx on the dms_app
// pool) and then exercises the REAL repository exactly as
// services/search/cmd/server/main.go constructs it (repository.New(pool)
// over the app pool). It asserts the CORRECT post-fix behavior — the reads
// must return the seeded rows — so today it FAILS with the RLS fail-closed
// symptom. It is allow-listed in ci/prod-posture-allowlist.txt until the
// Wave A fix lands; remove the allowlist entry when this goes green.
package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/search/internal/repository"
)

func TestProdPosture_SearchSavedSearchRepo(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	// Real search-service migrations (000001..000004): saved_searches and
	// saved_search_subscribers with ENABLE + FORCE ROW LEVEL SECURITY and
	// the app.current_tenant isolation policies. No cross-service parent
	// rows are required — neither table carries a foreign key.
	db := testutil.NewProdPostureDB(ctx, t, "../../migrations")

	tenantID := uuid.Must(uuid.NewV7())
	userID := uuid.Must(uuid.NewV7())
	searchID := uuid.Must(uuid.NewV7())

	// Seed one saved search + one subscriber the tenant-correct way:
	// WithTenantTx on the dms_app pool sets SET LOCAL app.current_tenant,
	// so the FORCE-RLS INSERT policies accept the rows.
	require.NoError(t, database.WithTenantTx(ctx, db.App, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO saved_searches
			    (id, tenant_id, user_id, name, query, filters, notify, notify_interval_minutes, created_at)
			VALUES ($1, $2, $3, $4, $5, '{}'::jsonb, false, 15, now())
		`, searchID, tenantID, userID, "prod-posture repro", "contract"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO saved_search_subscribers
			    (tenant_id, saved_search_id, user_id, channels, subscribed_by, subscribed_at)
			VALUES ($1, $2, $3, ARRAY['in_app'], $3, now())
		`, tenantID, searchID, userID)
		return err
	}), "tenant-correct seeding must succeed under FORCE RLS")

	// Sanity: the rows really exist (superuser sees through RLS).
	var seeded int
	require.NoError(t, db.Super.QueryRow(ctx,
		`SELECT COUNT(*) FROM saved_searches WHERE id = $1`, searchID).Scan(&seeded))
	require.Equal(t, 1, seeded, "seed row must exist in the table")

	// Drop the pooled connections used for seeding. A local set_config on a
	// previously-undefined custom GUC leaves it defined as '' at session
	// level after commit, and these policies cast ::uuid, so a REUSED
	// connection errors 22P02 instead of filtering. On a FRESH connection
	// current_setting('app.current_tenant', true) is NULL and the read
	// fails closed (0 rows) — the canonical prod symptom the audit
	// describes. Reset pins the deterministic fail-closed mode.
	db.App.Reset()

	// The REAL repository, constructed exactly as production main.go does
	// (services/search/cmd/server/main.go: repo := repository.New(pool))
	// over the NOBYPASSRLS app pool. The repo never sets
	// app.current_tenant, so under prod posture every read below fails
	// closed today — these assertions state the correct post-fix behavior.
	repo := repository.New(db.App)

	t.Run("ListSavedSearches", func(t *testing.T) {
		got, err := repo.ListSavedSearches(ctx, tenantID.String(), userID.String())
		require.NoError(t, err)
		require.Len(t, got, 1,
			"ListSavedSearches must return the seeded saved search; 0 rows means "+
				"the repo queried the raw pool without app.current_tenant (RLS fail-closed)")
		require.Equal(t, searchID.String(), got[0].ID)
		require.Equal(t, "prod-posture repro", got[0].Name)
	})

	t.Run("GetSavedSearch", func(t *testing.T) {
		got, err := repo.GetSavedSearch(ctx, tenantID.String(), userID.String(), searchID.String())
		require.NoError(t, err,
			"GetSavedSearch must find the seeded row; ErrNotFound means RLS fail-closed "+
				"(raw pool, no tenant context)")
		require.Equal(t, searchID.String(), got.ID)
	})

	t.Run("ListSubscribers", func(t *testing.T) {
		got, err := repo.ListSubscribers(ctx, tenantID.String(), searchID.String())
		require.NoError(t, err)
		require.Len(t, got, 1,
			"ListSubscribers must return the seeded subscriber; 0 rows means "+
				"saved_search_alerts.go queried the raw pool without app.current_tenant")
		require.Equal(t, userID.String(), got[0].UserID)
	})
}
