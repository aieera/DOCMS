//go:build integration && prodposture
// +build integration,prodposture

// Prod-posture repro: billing repository ignores tenant context (RLS).
//
// BUG: services/billing/internal/repository/repository.go does every
// read and write on the raw *pgxpool.Pool (repository.New(pool) —
// exactly how services/billing/cmd/server/main.go:80 wires it in prod).
// subscriptions and usage_records are ENABLE + FORCE ROW LEVEL SECURITY
// with policies on current_setting('app.current_tenant'), and the prod
// role (dms_app) is NOBYPASSRLS. So in prod every billing write fails
// with "new row violates row-level security policy" and every read
// fails closed to 0 rows. Dev/integration runs mask this by connecting
// as the BYPASSRLS testcontainer superuser.
//
// Audit ref: STATE_OF_THE_PROJECT 2026-07-03 (RLS tenant-context gaps).
// Tracking:  https://github.com/aieera/DOCMS/issues/72
//
// This test asserts the CORRECT (post-fix) behavior — repository writes
// succeed under the enforced NOBYPASSRLS posture and a tenant-correct
// read sees the row — so it FAILS today. It is allow-listed in
// ci/prod-posture-allowlist.txt until the Wave A fix lands; the fix PR
// must remove that line and keep this test as the regression guard.
//
// Run:
//
//	go test -tags "integration prodposture" -run '^TestProdPosture_BillingRepo$' \
//	    ./services/billing/internal/repository/
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
	"github.com/aieera/sedoc/services/billing/internal/model"
	"github.com/aieera/sedoc/services/billing/internal/repository"
)

// billingBaselineDDL is the prerequisite schema for the billing
// migration chain. Billing's own migrations start at
// 000010_reconcile_billing_schema, which renames/reshapes tables
// created by the document service's 000001_initial_schema (shared
// database in prod). The DDL below is copied verbatim from that
// migration (organizations + TABLE 40 subscriptions_billing + TABLE 41
// usage_meters + the updated_at trigger helper) so the REAL billing
// migrations can run on top of it against a clean container.
const billingBaselineDDL = `
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TABLE organizations (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name           TEXT NOT NULL,
    slug           TEXT NOT NULL UNIQUE,
    plan           TEXT NOT NULL DEFAULT 'standard'
                        CHECK (plan IN ('standard', 'enterprise', 'dedicated')),
    settings       JSONB NOT NULL DEFAULT '{}'::jsonb,
    primary_region TEXT NOT NULL DEFAULT 'us-east-1',
    logo_url       TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at     TIMESTAMPTZ
);
CREATE INDEX idx_organizations_slug ON organizations(slug) WHERE deleted_at IS NULL;
CREATE TRIGGER update_organizations_updated_at
    BEFORE UPDATE ON organizations
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TABLE subscriptions_billing (
    tenant_id             UUID PRIMARY KEY REFERENCES organizations(id),
    plan                  TEXT NOT NULL DEFAULT 'standard',
    stripe_customer_id    TEXT,
    stripe_subscription_id TEXT,
    status                TEXT NOT NULL DEFAULT 'active'
                               CHECK (status IN ('active', 'trialing', 'past_due', 'cancelled', 'suspended')),
    user_limit            INT,
    storage_limit_gb      INT,
    current_period_start  TIMESTAMPTZ,
    current_period_end    TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_sub_billing_stripe_customer ON subscriptions_billing(stripe_customer_id) WHERE stripe_customer_id IS NOT NULL;
CREATE TRIGGER update_subscriptions_billing_updated_at
    BEFORE UPDATE ON subscriptions_billing FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
ALTER TABLE subscriptions_billing ENABLE ROW LEVEL SECURITY;
ALTER TABLE subscriptions_billing FORCE  ROW LEVEL SECURITY;
CREATE POLICY sub_billing_tenant_isolation ON subscriptions_billing
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY sub_billing_tenant_isolation_insert ON subscriptions_billing
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE usage_meters (
    tenant_id     UUID NOT NULL REFERENCES organizations(id),
    id            UUID NOT NULL DEFAULT gen_random_uuid(),
    metric        TEXT NOT NULL CHECK (metric IN
                       ('storage_gb_days', 'ocr_pages', 'api_calls', 'signatures', 'ai_tokens', 'active_users')),
    quantity      NUMERIC NOT NULL,
    period_start  DATE NOT NULL,
    period_end    DATE NOT NULL,
    recorded_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);
CREATE INDEX idx_usage_meters_period ON usage_meters(tenant_id, metric, period_start);
ALTER TABLE usage_meters ENABLE ROW LEVEL SECURITY;
ALTER TABLE usage_meters FORCE  ROW LEVEL SECURITY;
CREATE POLICY usage_meters_tenant_isolation ON usage_meters
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY usage_meters_tenant_isolation_insert ON usage_meters
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
`

func TestProdPosture_BillingRepo(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	// Container + dms_app (NOBYPASSRLS, posture gate armed). Migrations
	// dir is empty here because billing's chain needs the baseline first.
	db := testutil.NewProdPostureDB(ctx, t, "")

	_, err := db.Super.Exec(ctx, billingBaselineDDL)
	require.NoError(t, err, "seed billing baseline schema (document 000001 subset)")

	// Now the REAL billing migrations (000010 reshapes the baseline into
	// subscriptions/usage_records, carrying FORCE RLS + policies along).
	require.NoError(t, database.RunMigrations(db.SuperDSN, "../../migrations"),
		"billing migrations must apply cleanly on the baseline")

	// Re-issue the grants NewProdPostureDB gave: they ran before these
	// tables existed. Same grant set as migration 000065/init-db.sql.
	_, err = db.Super.Exec(ctx, `
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dms_app;
		GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO dms_app;
	`)
	require.NoError(t, err)

	// Out-of-band tenant fixture (organizations is the tenant table, no
	// RLS; usage_records/subscriptions FK to it).
	tenant := uuid.Must(uuid.NewV7())
	_, err = db.Super.Exec(ctx,
		`INSERT INTO organizations (id, name, slug, plan) VALUES ($1, 'Prod Posture Org', $2, 'standard')`,
		tenant, "prodposture-"+tenant.String())
	require.NoError(t, err)

	// The REAL repository over the app pool — identical to prod wiring
	// (services/billing/cmd/server/main.go:80: repository.New(pool)).
	repo := repository.New(db.App)

	// ---- WRITE path (repository.go InsertUsage, raw pool) ------------
	// Correct behavior: the metering write succeeds for the tenant.
	// Today: "new row violates row-level security policy for table
	// \"usage_records\"" because no app.current_tenant is set and
	// dms_app cannot bypass FORCE RLS.
	periodStart := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	usage := &model.UsageRecord{
		TenantID:    tenant.String(),
		PeriodStart: periodStart,
		PeriodEnd:   periodStart.AddDate(0, 1, 0),
		StorageGB:   12.5,
		OCRPages:    340,
		APICalls:    9100,
		ActiveUsers: 7,
		AITokens:    120_000,
	}
	require.NoError(t, repo.InsertUsage(ctx, usage),
		"InsertUsage must succeed under the prod NOBYPASSRLS posture (issue #72)")

	// The row must be visible to a tenant-correct read.
	var usageRows int
	require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM usage_records WHERE tenant_id = $1`, tenant).Scan(&usageRows)
	}))
	require.Equal(t, 1, usageRows, "tenant-context read must see the usage row")

	// ---- Second flagged WRITE + fail-closed READ (subscriptions) -----
	now := time.Now().UTC()
	sub := &model.Subscription{
		TenantID:           tenant.String(),
		PlanID:             "standard",
		Status:             "active",
		CurrentPeriodStart: now,
		CurrentPeriodEnd:   now.AddDate(0, 1, 0),
		CreatedAt:          now,
	}
	require.NoError(t, repo.UpsertSubscription(ctx, sub),
		"UpsertSubscription must succeed under the prod NOBYPASSRLS posture (issue #72)")

	// GetSubscription is the same raw-pool bug on the read side: under
	// FORCE RLS with no tenant GUC it fails closed and returns nil.
	got, err := repo.GetSubscription(ctx, tenant.String())
	require.NoError(t, err)
	require.NotNil(t, got,
		"GetSubscription must see the tenant's subscription (raw-pool read currently fails closed to 0 rows)")
	require.Equal(t, "standard", got.PlanID)

	// ---- Per-tenant isolation (Wave A.1.c DoD) ------------------------
	// A second tenant must see neither the first tenant's subscription
	// nor its usage rows — and its own writes must not leak back.
	tenantB := uuid.Must(uuid.NewV7())
	_, err = db.Super.Exec(ctx,
		`INSERT INTO organizations (id, name, slug, plan) VALUES ($1, 'Prod Posture Org B', $2, 'standard')`,
		tenantB, "prodposture-b-"+tenantB.String())
	require.NoError(t, err)

	gotB, err := repo.GetSubscription(ctx, tenantB.String())
	require.NoError(t, err)
	require.Nil(t, gotB, "tenant B must not see tenant A's subscription")

	subB := &model.Subscription{
		TenantID:           tenantB.String(),
		PlanID:             "enterprise",
		Status:             "active",
		CurrentPeriodStart: now,
		CurrentPeriodEnd:   now.AddDate(0, 1, 0),
		CreatedAt:          now,
	}
	require.NoError(t, repo.UpsertSubscription(ctx, subB))

	// Each tenant reads exactly its own plan back.
	gotA, err := repo.GetSubscription(ctx, tenant.String())
	require.NoError(t, err)
	require.Equal(t, "standard", gotA.PlanID)
	gotB, err = repo.GetSubscription(ctx, tenantB.String())
	require.NoError(t, err)
	require.Equal(t, "enterprise", gotB.PlanID)

	// Tenant B's context must see zero of A's usage rows (RLS, not just
	// the SQL predicate).
	var crossRows int
	require.NoError(t, database.WithTenantTx(ctx, db.App, tenantB, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COUNT(*) FROM usage_records`).Scan(&crossRows)
	}))
	require.Zero(t, crossRows, "tenant B's context must not see tenant A's usage rows")
}
