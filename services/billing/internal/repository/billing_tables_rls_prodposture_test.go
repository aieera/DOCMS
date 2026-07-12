//go:build integration && prodposture
// +build integration,prodposture

// Prod-posture RLS posture for EVERY billing table + the service-level plan
// transition trial→paid→canceled→resubscribed, under the enforced
// NOBYPASSRLS role.
//
// TestProdPosture_BillingRepo proves subscriptions + usage_records isolate
// functionally. This test makes the "all billing tables" guarantee explicit
// and future-proof:
//
//   - It enumerates the FORCE-RLS tables from the catalog and asserts the
//     set is exactly the two tenant-scoped billing tables — so a new
//     FORCE-RLS billing table added later trips this test until it's given
//     isolation coverage — then asserts a second tenant's context sees zero
//     of the first tenant's rows in each.
//   - It asserts the platform lookup tables (stripe_customer_map,
//     stripe_processed_events) are DELIBERATELY non-RLS and resolve
//     pre-tenant on the raw pool — the money path breaks if they don't
//     (the webhook resolves the tenant FROM them before any tenant ctx).
//   - It drives a subscription through trialing→active→cancelled→active
//     over the REAL repository, proving each transition persists under RLS
//   - the status CHECK constraint.
//
// billingBaselineDDL is shared with prod_posture_repro_test.go (same package).
package repository_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/billing/internal/model"
	"github.com/aieera/sedoc/services/billing/internal/repository"
)

func setupBillingSchema(ctx context.Context, t *testing.T) (*testutil.ProdPostureDB, *repository.Repository) {
	t.Helper()
	db := testutil.NewProdPostureDB(ctx, t, "")
	_, err := db.Super.Exec(ctx, billingBaselineDDL)
	require.NoError(t, err, "seed billing baseline schema")
	require.NoError(t, database.RunMigrations(db.SuperDSN, "../../migrations"), "billing migrations")
	_, err = db.Super.Exec(ctx, `
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dms_app;
		GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO dms_app;`)
	require.NoError(t, err)
	return db, repository.New(db.App)
}

func TestProdPosture_AllBillingTablesRLSPosture(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	db, repo := setupBillingSchema(ctx, t)

	tenantA := uuid.Must(uuid.NewV7())
	tenantB := uuid.Must(uuid.NewV7())
	_, err := db.Super.Exec(ctx, `INSERT INTO organizations (id, name, slug, plan) VALUES
		($1,'A',$3,'standard'), ($2,'B',$4,'standard')`,
		tenantA, tenantB, "rls-a-"+tenantA.String(), "rls-b-"+tenantB.String())
	require.NoError(t, err)

	// --- 1. Enumerate the FORCE-RLS tables; the set must be exactly the two
	// tenant-scoped billing tables. A new RLS billing table without coverage
	// trips this. ------------------------------------------------------------
	var rlsTables []string
	rows, err := db.Super.Query(ctx, `
		SELECT relname FROM pg_class
		WHERE relrowsecurity AND relkind = 'r' AND relnamespace = 'public'::regnamespace
		ORDER BY relname`)
	require.NoError(t, err)
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		rlsTables = append(rlsTables, n)
	}
	require.NoError(t, rows.Err())
	sort.Strings(rlsTables)
	require.Equal(t, []string{"subscriptions", "usage_records"}, rlsTables,
		"the FORCE-RLS billing tables must be exactly these two — a new one needs isolation coverage here")

	// --- 2. Per-RLS-table isolation: seed tenant A only, assert tenant B's
	// context sees zero. -----------------------------------------------------
	now := time.Now().UTC()
	require.NoError(t, repo.UpsertSubscription(ctx, &model.Subscription{
		TenantID: tenantA.String(), PlanID: "standard", Status: "active",
		CurrentPeriodStart: now, CurrentPeriodEnd: now.AddDate(0, 1, 0), CreatedAt: now,
	}))
	require.NoError(t, repo.InsertUsage(ctx, &model.UsageRecord{
		TenantID: tenantA.String(), PeriodStart: now.Truncate(time.Hour),
		PeriodEnd: now.Truncate(time.Hour).Add(time.Hour), StorageGB: 5,
	}))

	for _, tbl := range rlsTables {
		var bCount, aCount int
		require.NoError(t, database.WithTenantTx(ctx, db.App, tenantB, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT COUNT(*) FROM `+tbl).Scan(&bCount)
		}))
		require.Zerof(t, bCount, "tenant B must see zero rows in %s (tenant A's data)", tbl)
		require.NoError(t, database.WithTenantTx(ctx, db.App, tenantA, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT COUNT(*) FROM `+tbl).Scan(&aCount)
		}))
		require.Equalf(t, 1, aCount, "tenant A must see its own row in %s", tbl)
	}

	// --- 3. Platform lookup tables are DELIBERATELY non-RLS and resolve
	// pre-tenant (the webhook derives the tenant from them before any tenant
	// context exists). -------------------------------------------------------
	for _, tbl := range []string{"stripe_customer_map", "stripe_processed_events"} {
		var hasRLS bool
		require.NoError(t, db.Super.QueryRow(ctx,
			`SELECT relrowsecurity FROM pg_class WHERE relname = $1`, tbl).Scan(&hasRLS))
		require.Falsef(t, hasRLS, "%s must stay non-RLS (pre-tenant lookup anchor)", tbl)
	}
	// And they actually work on the raw pool under NOBYPASSRLS.
	require.NoError(t, repo.UpsertCustomerMap(ctx, "cus_rls", "sub_rls", tenantA.String()))
	gotTenant, gotSub, found, err := repo.TenantByStripeCustomer(ctx, "cus_rls")
	require.NoError(t, err)
	require.True(t, found, "stripe_customer_map must resolve pre-tenant on the raw pool")
	require.Equal(t, tenantA.String(), gotTenant)
	require.Equal(t, "sub_rls", gotSub)

	done, err := repo.StripeEventAlreadyProcessed(ctx, "evt_rls")
	require.NoError(t, err)
	require.False(t, done)
	require.NoError(t, repo.MarkStripeEventProcessed(ctx, "evt_rls", "invoice.paid"))
	done, err = repo.StripeEventAlreadyProcessed(ctx, "evt_rls")
	require.NoError(t, err)
	require.True(t, done, "stripe_processed_events dedupe must work pre-tenant on the raw pool")
}

// The plan lifecycle persisted through the REAL repository under RLS: every
// transition must survive the status CHECK constraint + the tenant policy.
func TestProdPosture_BillingPlanTransitions_TrialToPaidToCanceledToResubscribed(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	db, repo := setupBillingSchema(ctx, t)

	tenant := uuid.Must(uuid.NewV7())
	_, err := db.Super.Exec(ctx,
		`INSERT INTO organizations (id, name, slug, plan) VALUES ($1,'LC',$2,'standard')`,
		tenant, "lc-"+tenant.String())
	require.NoError(t, err)

	now := time.Now().UTC()
	statusOf := func() string {
		sub, err := repo.GetSubscription(ctx, tenant.String())
		require.NoError(t, err)
		require.NotNil(t, sub)
		return sub.Status
	}

	// trial
	require.NoError(t, repo.UpsertSubscription(ctx, &model.Subscription{
		TenantID: tenant.String(), PlanID: "standard", Status: "trialing",
		CurrentPeriodStart: now, CurrentPeriodEnd: now.AddDate(0, 0, 14), CreatedAt: now,
	}))
	require.Equal(t, "trialing", statusOf())

	// paid
	require.NoError(t, repo.UpdateSubscriptionStatus(ctx, tenant.String(), "active", nil))
	require.Equal(t, "active", statusOf())

	// canceled
	require.NoError(t, repo.UpdateSubscriptionStatus(ctx, tenant.String(), "cancelled", nil))
	require.Equal(t, "cancelled", statusOf())

	// resubscribed — the upsert reactivates the same tenant row.
	require.NoError(t, repo.UpsertSubscription(ctx, &model.Subscription{
		TenantID: tenant.String(), PlanID: "standard", Status: "active",
		CurrentPeriodStart: now, CurrentPeriodEnd: now.AddDate(0, 1, 0), CreatedAt: now,
	}))
	require.Equal(t, "active", statusOf())
}
