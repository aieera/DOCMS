//go:build integration && prodposture
// +build integration,prodposture

// Wave A.1.a (issue #70) — the alert-schedule sweeps. Reconcile/
// RegisterSavedSearchAlertSchedules legitimately need every tenant's
// alert rows, but before the fix they full-table-scanned saved_searches
// on the raw pool: under prod NOBYPASSRLS that returns 0 rows and no
// schedule is ever created or reconciled. The sanctioned shape (task
// item 2) is: enumerate tenants from the non-RLS organizations registry,
// then read each tenant's rows under its own app.current_tenant —
// never SET row_security = off.
package workflows_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/workflow/internal/workflows"
)

func TestProdPosture_AlertScheduleSweepPerTenant(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)

	db := testutil.NewProdPostureDB(ctx, t, "")
	require.NoError(t, database.RunServiceMigrations(db.SuperDSN, "../../../search/migrations", "search"))

	// organizations is the tenant registry — deliberately NO RLS (it is
	// the enumeration anchor for cross-tenant maintenance loops).
	_, err := db.Super.Exec(ctx, `
		CREATE TABLE organizations (
			id             UUID PRIMARY KEY,
			name           TEXT NOT NULL,
			slug           TEXT NOT NULL UNIQUE,
			plan           TEXT NOT NULL,
			primary_region TEXT NOT NULL
		);
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dms_app;
		GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO dms_app;
	`)
	require.NoError(t, err)

	tenantA := uuid.Must(uuid.NewV7())
	tenantB := uuid.Must(uuid.NewV7())
	_, err = db.Super.Exec(ctx, `INSERT INTO organizations (id, name, slug, plan, primary_region) VALUES
		($1,'A','sweep-a','standard','us-east-1'),
		($2,'B','sweep-b','standard','us-east-1')`, tenantA, tenantB)
	require.NoError(t, err)

	seed := func(tenant uuid.UUID, name string, notify bool) uuid.UUID {
		id := uuid.Must(uuid.NewV7())
		require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
				INSERT INTO saved_searches (id, tenant_id, user_id, name, query, filters, notify, notify_interval_minutes, created_at)
				VALUES ($1, $2, $3, $4, 'q', '{}'::jsonb, $5, 15, now())`,
				id, tenant, uuid.Must(uuid.NewV7()), name, notify)
			return err
		}))
		return id
	}
	aAlert := seed(tenantA, "a-alert", true)
	bAlert := seed(tenantB, "b-alert", true)
	seed(tenantB, "b-muted", false)

	// The sweep's DB phase must see BOTH tenants' rows under the
	// NOBYPASSRLS role, via per-tenant context — this is what feeds
	// Reconcile/RegisterSavedSearchAlertSchedules.
	all, err := workflows.CollectSavedSearchAlertRows(ctx, db.App, false)
	require.NoError(t, err)
	require.Len(t, all, 3, "sweep must enumerate every tenant's rows via per-tenant context")

	notifiable, err := workflows.CollectSavedSearchAlertRows(ctx, db.App, true)
	require.NoError(t, err)
	require.Len(t, notifiable, 2)
	got := map[string]string{}
	for _, r := range notifiable {
		got[r.ID] = r.TenantID
	}
	require.Equal(t, tenantA.String(), got[aAlert.String()])
	require.Equal(t, tenantB.String(), got[bAlert.String()])
}
