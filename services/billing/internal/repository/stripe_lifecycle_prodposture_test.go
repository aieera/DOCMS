//go:build integration && prodposture
// +build integration,prodposture

// Prod-posture money-path lifecycle: a full Stripe lifecycle (checkout →
// invoice → subscription.updated → cancel) must be reflected in local
// state under the enforced NOBYPASSRLS posture — the real fix.
//
// The webhook resolves the tenant from the NON-RLS stripe_customer_map
// (works pre-tenant under dms_app), then writes the FORCE-RLS
// subscriptions row inside a tenant tx. This test drives the real
// repository over the dms_app pool exactly as prod wires it.
package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/billing/internal/model"
	"github.com/aieera/sedoc/services/billing/internal/repository"
)

func TestProdPosture_StripeLifecycle(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	db := testutil.NewProdPostureDB(ctx, t, "")
	_, err := db.Super.Exec(ctx, billingBaselineDDL)
	require.NoError(t, err, "seed billing baseline schema")
	require.NoError(t, database.RunMigrations(db.SuperDSN, "../../migrations"),
		"billing migrations (incl. 000011 stripe map) must apply")
	_, err = db.Super.Exec(ctx, `
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dms_app;
		GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO dms_app;`)
	require.NoError(t, err)

	tenant := uuid.Must(uuid.NewV7())
	_, err = db.Super.Exec(ctx,
		`INSERT INTO organizations (id, name, slug, plan) VALUES ($1, 'Money Path Org', $2, 'standard')`,
		tenant, "moneypath-"+tenant.String())
	require.NoError(t, err)

	// The REAL repository over the app (NOBYPASSRLS) pool — prod wiring.
	repo := repository.New(db.App)
	const cust = "cus_prod_1"
	const subID = "sub_prod_1"

	// ---- checkout.session.completed ----
	// 1. Non-RLS map write (the pre-tenant anchor).
	require.NoError(t, repo.UpsertCustomerMap(ctx, cust, subID, tenant.String()),
		"customer map write must succeed under NOBYPASSRLS")
	// 2. Tenant-scoped subscription write.
	now := time.Now().UTC()
	require.NoError(t, repo.UpsertSubscription(ctx, &model.Subscription{
		TenantID: tenant.String(), PlanID: "standard",
		StripeCustomerID: cust, StripeSubID: subID, Status: "active",
		CurrentPeriodStart: now, CurrentPeriodEnd: now.AddDate(0, 1, 0), CreatedAt: now,
	}), "subscription write must succeed under NOBYPASSRLS")

	// The webhook can now resolve the tenant from either Stripe id
	// pre-tenant (this is what was impossible before).
	gotTenant, gotSub, found, err := repo.TenantByStripeCustomer(ctx, cust)
	require.NoError(t, err)
	require.True(t, found, "customer must resolve to a tenant pre-tenant-context")
	require.Equal(t, tenant.String(), gotTenant)
	require.Equal(t, subID, gotSub)

	bySub, found, err := repo.TenantByStripeSubscription(ctx, subID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, tenant.String(), bySub)

	// ---- invoice.payment_failed → past_due ----
	grace := now.AddDate(0, 0, 7)
	require.NoError(t, repo.UpdateSubscriptionStatus(ctx, tenant.String(), "past_due", &grace))
	sub, err := repo.GetSubscription(ctx, tenant.String())
	require.NoError(t, err)
	require.NotNil(t, sub)
	require.Equal(t, "past_due", sub.Status)

	// ---- invoice.paid → active ----
	require.NoError(t, repo.UpdateSubscriptionStatus(ctx, tenant.String(), "active", nil))
	sub, err = repo.GetSubscription(ctx, tenant.String())
	require.NoError(t, err)
	require.Equal(t, "active", sub.Status)

	// ---- customer.subscription.deleted → cancelled ----
	require.NoError(t, repo.UpdateSubscriptionStatus(ctx, tenant.String(), "cancelled", nil))
	sub, err = repo.GetSubscription(ctx, tenant.String())
	require.NoError(t, err)
	require.Equal(t, "cancelled", sub.Status,
		"the full lifecycle must be reflected in local state under prod posture")

	// ---- idempotency dedupe (non-RLS) ----
	done, err := repo.StripeEventAlreadyProcessed(ctx, "evt_lc_1")
	require.NoError(t, err)
	require.False(t, done)
	require.NoError(t, repo.MarkStripeEventProcessed(ctx, "evt_lc_1", "checkout.session.completed"))
	done, err = repo.StripeEventAlreadyProcessed(ctx, "evt_lc_1")
	require.NoError(t, err)
	require.True(t, done, "a processed event id must dedupe on redelivery")
}
