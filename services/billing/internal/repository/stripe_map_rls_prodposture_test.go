//go:build integration && prodposture
// +build integration,prodposture

// stripe_customer_map under RLS (M-28, billing 000012).
//
// The map is a tenant table: it carries tenant_id, so it is ENABLE + FORCE
// ROW LEVEL SECURITY like every other one. The webhook still has to learn
// the tenant FROM a Stripe id before any tenant context exists; that read
// goes through the exact-match SECURITY DEFINER lookups of 000012 (DEC-2).
// Everything after the tenant is known — the map write itself, the
// subscription row — runs inside WithTenantTx.
package repository_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/testutil"
	stripehandler "github.com/aieera/sedoc/services/billing/internal/stripe"
)

func postStripeEvent(t *testing.T, wh *stripehandler.WebhookHandler, body string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/stripe/webhook", strings.NewReader(body))
	rec := httptest.NewRecorder()
	wh.HandleWebhook(rec, req)
	return rec.Code
}

func TestProdPosture_StripeWebhookMapsTenant(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	db, repo := setupBillingSchema(ctx, t)

	// The map is a FORCE-RLS tenant table now.
	var enabled, forced bool
	require.NoError(t, db.Super.QueryRow(ctx, `
		SELECT relrowsecurity, relforcerowsecurity FROM pg_class
		 WHERE oid = 'public.stripe_customer_map'::regclass`).Scan(&enabled, &forced))
	require.True(t, enabled && forced, "stripe_customer_map must be ENABLE + FORCE ROW LEVEL SECURITY")

	tenantA, tenantB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	_, err := db.Super.Exec(ctx, `INSERT INTO organizations (id, name, slug, plan) VALUES
		($1,'A',$3,'standard'), ($2,'B',$4,'standard')`,
		tenantA, tenantB, "map-a-"+tenantA.String(), "map-b-"+tenantB.String())
	require.NoError(t, err)

	// The real webhook over the dms_app pool (no secret: the dev JSON path;
	// these events never touch the tenant router).
	wh := stripehandler.NewWebhookHandler(repo, nil, zerolog.Nop(), "")

	// 1. checkout.session.completed maps cus_A/sub_A → tenant A.
	code := postStripeEvent(t, wh, `{"id":"evt_co_a","type":"checkout.session.completed","data":{"object":{
		"object":"checkout.session","client_reference_id":"`+tenantA.String()+`",
		"customer":"cus_A","subscription":"sub_A"}}}`)
	require.Equal(t, http.StatusOK, code, "checkout must persist the map under NOBYPASSRLS")

	// 2. A later event carrying only the Stripe subscription resolves tenant A
	//    pre-tenant and updates A's subscription.
	code = postStripeEvent(t, wh, `{"id":"evt_pf_a","type":"invoice.payment_failed","data":{"object":{
		"object":"invoice","subscription":"sub_A","customer":"cus_A"}}}`)
	require.Equal(t, http.StatusOK, code)
	sub, err := repo.GetSubscription(ctx, tenantA.String())
	require.NoError(t, err)
	require.NotNil(t, sub)
	require.Equal(t, "past_due", sub.Status, "the webhook must map sub_A to tenant A")

	// 3. Customer-only resolution (invoice without a subscription id).
	tid, subID, found, err := repo.TenantByStripeCustomer(ctx, "cus_A")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, tenantA.String(), tid)
	require.Equal(t, "sub_A", subID)

	// 4. Tenant B's checkout cannot re-point A's Stripe customer at B.
	code = postStripeEvent(t, wh, `{"id":"evt_co_b","type":"checkout.session.completed","data":{"object":{
		"object":"checkout.session","client_reference_id":"`+tenantB.String()+`",
		"customer":"cus_A","subscription":"sub_B"}}}`)
	require.NotEqual(t, http.StatusOK, code, "a foreign tenant must not take over another tenant's customer")
	tid, _, found, err = repo.TenantByStripeCustomer(ctx, "cus_A")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, tenantA.String(), tid, "cus_A must still map to tenant A")

	// 5. The table itself is fail-closed for dms_app: no tenant context → 0
	//    rows; tenant B's context → none of A's rows.
	conn, err := pgx.Connect(ctx, db.AppDSN)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	var n int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM stripe_customer_map`).Scan(&n))
	require.Zero(t, n, "no tenant context must see no map rows")
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenantB.String())
	require.NoError(t, err)
	require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM stripe_customer_map`).Scan(&n))
	require.Zero(t, n, "tenant B must not see tenant A's mapping")
	require.NoError(t, tx.Rollback(ctx))

	// 6. Unknown ids resolve to not-found, not an error.
	_, found, err = repo.TenantByStripeSubscription(ctx, "sub_missing")
	require.NoError(t, err)
	require.False(t, found)
	_, _, found, err = repo.TenantByStripeCustomer(ctx, "cus_missing")
	require.NoError(t, err)
	require.False(t, found)
}
