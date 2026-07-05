//go:build integration && prodposture
// +build integration,prodposture

// Wave A.1.c (issue #72) — the usage-metering cron under the prod
// NOBYPASSRLS posture. Before the fix: every meter read ran on the raw
// pool (fail-closed to 0 under FORCE RLS), InsertUsage was rejected by
// the RLS policy each cycle, MeterActiveUsers queried a column that
// does not exist (sessions.last_active_at vs last_activity_at), and the
// meter-read errors were silently discarded — so prod would have logged
// an "insert usage" error every cycle while active_users metered 0
// forever, silently, in dev.
//
// This test drives the REAL collect loop for 5+ cycles against a
// dms_app NOBYPASSRLS database with two tenants and asserts: zero
// error-level log lines across all cycles, per-tenant usage rows with
// the correct (non-zero, non-leaking) meter values.
//
// Internal package test so it can drive collect() directly instead of
// waiting on the hourly ticker.
package metering

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/billing/internal/repository"
)

// meteringFixtureDDL: the billing baseline (document-000001 subset, same
// as the repository prod-posture test) plus minimal FORCE-RLS versions
// of the document-owned tables the meters read (content_blobs,
// ocr_results, sessions — column shapes match the meter queries and the
// schema of record, including sessions.last_activity_at).
const meteringFixtureDDL = `
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

-- Meter-source tables (document-owned in prod; minimal shapes).
CREATE TABLE content_blobs (
    tenant_id  UUID   NOT NULL,
    id         UUID   NOT NULL DEFAULT gen_random_uuid(),
    size_bytes BIGINT NOT NULL,
    PRIMARY KEY (tenant_id, id)
);
ALTER TABLE content_blobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE content_blobs FORCE  ROW LEVEL SECURITY;
CREATE POLICY content_blobs_tenant_isolation ON content_blobs
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE ocr_results (
    tenant_id  UUID        NOT NULL,
    id         UUID        NOT NULL DEFAULT gen_random_uuid(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);
ALTER TABLE ocr_results ENABLE ROW LEVEL SECURITY;
ALTER TABLE ocr_results FORCE  ROW LEVEL SECURITY;
CREATE POLICY ocr_results_tenant_isolation ON ocr_results
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE sessions (
    tenant_id        UUID        NOT NULL,
    id               UUID        NOT NULL DEFAULT gen_random_uuid(),
    user_id          UUID        NOT NULL,
    last_activity_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);
ALTER TABLE sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE sessions FORCE  ROW LEVEL SECURITY;
CREATE POLICY sessions_tenant_isolation ON sessions
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
`

func TestProdPosture_BillingMeteringCronCycles(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)

	db := testutil.NewProdPostureDB(ctx, t, "")
	_, err := db.Super.Exec(ctx, meteringFixtureDDL)
	require.NoError(t, err)
	require.NoError(t, database.RunMigrations(db.SuperDSN, "../../migrations"))
	_, err = db.Super.Exec(ctx, `
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dms_app;
		GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO dms_app;
	`)
	require.NoError(t, err)

	tenantA := uuid.Must(uuid.NewV7())
	tenantB := uuid.Must(uuid.NewV7())
	_, err = db.Super.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES
		($1,'A','meter-a'), ($2,'B','meter-b')`, tenantA, tenantB)
	require.NoError(t, err)

	// Per-tenant meter sources, seeded tenant-correctly: A has 2 GiB of
	// blobs, 3 OCR pages, 2 active users; B has 1 GiB, 1 page, 1 user.
	seed := func(tenant uuid.UUID, blobs []int64, ocrPages int, users int) {
		require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
			for _, b := range blobs {
				if _, err := tx.Exec(ctx,
					`INSERT INTO content_blobs (tenant_id, size_bytes) VALUES ($1, $2)`, tenant, b); err != nil {
					return err
				}
			}
			for i := 0; i < ocrPages; i++ {
				if _, err := tx.Exec(ctx,
					`INSERT INTO ocr_results (tenant_id) VALUES ($1)`, tenant); err != nil {
					return err
				}
			}
			for i := 0; i < users; i++ {
				if _, err := tx.Exec(ctx,
					`INSERT INTO sessions (tenant_id, user_id) VALUES ($1, $2)`, tenant, uuid.Must(uuid.NewV7())); err != nil {
					return err
				}
			}
			return nil
		}))
	}
	const gib = int64(1 << 30)
	seed(tenantA, []int64{gib, gib}, 3, 2)
	seed(tenantB, []int64{gib}, 1, 1)

	// The REAL cron body over the REAL repository on the dms_app pool,
	// exactly as main.go wires it — with the logger captured so "runs
	// clean" is assertable.
	var logBuf bytes.Buffer
	meter := New(repository.New(db.App), zerolog.New(&logBuf))

	const cycles = 6
	for i := 0; i < cycles; i++ {
		meter.collect(ctx)
	}

	require.NotContains(t, logBuf.String(), `"level":"error"`,
		"the metering cron must run clean for %d cycles under the prod posture; log output:\n%s",
		cycles, logBuf.String())
	require.Equal(t, cycles, strings.Count(logBuf.String(), "metering: collection complete"))

	// Per-tenant rows with correct, non-leaking meter values. InsertUsage
	// upserts per (tenant, period), so repeated cycles must not duplicate.
	type row struct {
		storageGB   float64
		ocrPages    int
		activeUsers int
		count       int
	}
	read := func(tenant uuid.UUID) row {
		var r row
		require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM usage_records`).Scan(&r.count); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `
				SELECT storage_gb, ocr_pages, active_users
				  FROM usage_records
				 WHERE tenant_id = $1
				 ORDER BY period_start DESC LIMIT 1`, tenant).
				Scan(&r.storageGB, &r.ocrPages, &r.activeUsers)
		}))
		return r
	}

	a := read(tenantA)
	require.InDelta(t, 2.0, a.storageGB, 0.01, "tenant A storage must meter 2 GiB")
	require.Equal(t, 3, a.ocrPages)
	require.Equal(t, 2, a.activeUsers,
		"active_users must be metered (the last_active_at column drift silently zeroed this before the fix)")

	b := read(tenantB)
	require.InDelta(t, 1.0, b.storageGB, 0.01)
	require.Equal(t, 1, b.ocrPages)
	require.Equal(t, 1, b.activeUsers)
	require.Equal(t, 1, b.count, "B's context sees only B's row(s) — no cross-tenant leak")
}
