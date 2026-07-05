//go:build integration && prodposture
// +build integration,prodposture

// Prod-only RLS repro for the connector repository (Wave A pin).
//
// BUG: services/connector/internal/repository/repository.go queries the
// raw *pgxpool.Pool without establishing app.current_tenant — every
// read method (ListWebhooks, GetConnector/ListConnectors, and the
// ListPendingDeliveries background delivery scan at ~:214) as well as
// the email/intake background scans. Dev masks this because the local
// role has BYPASSRLS; under the prod NOBYPASSRLS role the FORCE-RLS
// tables (webhook_subscriptions, webhook_deliveries, connector_configs
// — document migration 000001, tables 37-39) fail closed, so connector
// configs and webhooks read EMPTY and webhook delivery silently stops.
//
// Audit ref: docs/STATE_OF_THE_PROJECT.md (2026-07-03 full-audit
// baseline), RLS tenant-context gaps.
// Issue: https://github.com/aieera/DOCMS/issues/71
//
// This test asserts the CORRECT (post-fix) behavior — reads through the
// real repository over the dms_app pool must return the tenant's seeded
// rows — so it FAILS today with the RLS fail-closed mode (0 rows /
// not-found). It is allow-listed in ci/prod-posture-allowlist.txt until
// the Wave A repository fix lands; remove the allowlist entry with the
// fix.
//
// Fixture note: the connector tables' schema of record is the document
// service's 000001_initial_schema.up.sql, but that chain is currently
// broken on a clean DB by 000021 (STATE 2026-07-03 "Migration blocker"),
// so the base tables are created here verbatim (columns, PKs, FKs,
// CHECKs, ENABLE + FORCE ROW LEVEL SECURITY, tenant policies; triggers/
// indexes omitted as irrelevant to RLS) and then the connector service's
// OWN migrations (services/connector/migrations) are applied for real on
// top — exactly what the connector repo code expects at runtime.
//
// Run:
//
//	go test -tags "integration prodposture" \
//	  -run '^TestProdPosture_ConnectorRepo$' -timeout 5m \
//	  ./services/connector/internal/repository/
package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/connector/internal/repository"
)

func TestProdPosture_ConnectorRepo(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)

	// migrationsDir is empty on purpose: the connector's own migration
	// (000001_rename_active_and_events_type) only ALTERs tables that the
	// document schema-of-record creates, so it must run AFTER the base
	// DDL below — NewProdPostureDB would apply it against an empty DB
	// (guarded no-op) and golang-migrate's bookkeeping would then refuse
	// to re-apply it.
	db := testutil.NewProdPostureDB(ctx, t, "")

	// --- Base tables, verbatim from services/document/migrations/
	// 000001_initial_schema.up.sql tables 37-39 (+ minimal FK parents).
	// FORCE ROW LEVEL SECURITY + tenant policies are the load-bearing
	// part: they are exactly what prod enforces against dms_app.
	_, err := db.Super.Exec(ctx, `
		CREATE TABLE organizations (
			id UUID PRIMARY KEY
		);
		CREATE TABLE users (
			tenant_id UUID NOT NULL REFERENCES organizations(id),
			id        UUID NOT NULL,
			PRIMARY KEY (tenant_id, id)
		);

		-- TABLE 37: webhook_subscriptions (pre-connector-migration shape)
		CREATE TABLE webhook_subscriptions (
			tenant_id        UUID NOT NULL REFERENCES organizations(id),
			id               UUID NOT NULL DEFAULT gen_random_uuid(),
			url              TEXT NOT NULL,
			events           TEXT[] NOT NULL,
			secret           TEXT NOT NULL,
			is_active        BOOLEAN NOT NULL DEFAULT true,
			failure_count    INT NOT NULL DEFAULT 0,
			last_success_at  TIMESTAMPTZ,
			last_failure_at  TIMESTAMPTZ,
			created_by       UUID,
			created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (tenant_id, id),
			FOREIGN KEY (tenant_id, created_by) REFERENCES users(tenant_id, id)
		);
		ALTER TABLE webhook_subscriptions ENABLE ROW LEVEL SECURITY;
		ALTER TABLE webhook_subscriptions FORCE  ROW LEVEL SECURITY;
		CREATE POLICY webhook_subs_tenant_isolation ON webhook_subscriptions
			USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
		CREATE POLICY webhook_subs_tenant_isolation_insert ON webhook_subscriptions
			FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

		-- TABLE 38: webhook_deliveries
		CREATE TABLE webhook_deliveries (
			tenant_id        UUID NOT NULL REFERENCES organizations(id),
			id               UUID NOT NULL DEFAULT gen_random_uuid(),
			subscription_id  UUID NOT NULL,
			event_type       TEXT NOT NULL,
			event_id         TEXT NOT NULL,
			payload          JSONB NOT NULL,
			status           TEXT NOT NULL DEFAULT 'pending'
			                      CHECK (status IN ('pending', 'delivered', 'failed', 'dead_letter')),
			http_status      INT,
			attempts         INT NOT NULL DEFAULT 0,
			last_attempt_at  TIMESTAMPTZ,
			next_retry_at    TIMESTAMPTZ,
			error_message    TEXT,
			created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (tenant_id, id),
			FOREIGN KEY (tenant_id, subscription_id) REFERENCES webhook_subscriptions(tenant_id, id)
		);
		ALTER TABLE webhook_deliveries ENABLE ROW LEVEL SECURITY;
		ALTER TABLE webhook_deliveries FORCE  ROW LEVEL SECURITY;
		CREATE POLICY webhook_deliveries_tenant_isolation ON webhook_deliveries
			USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
		CREATE POLICY webhook_deliveries_tenant_isolation_insert ON webhook_deliveries
			FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

		-- TABLE 39: connector_configs
		CREATE TABLE connector_configs (
			tenant_id              UUID NOT NULL REFERENCES organizations(id),
			id                     UUID NOT NULL DEFAULT gen_random_uuid(),
			connector_type         TEXT NOT NULL CHECK (connector_type IN
			                            ('salesforce', 'sap', 'm365', 'google', 'servicenow', 'workday', 'custom')),
			display_name           TEXT NOT NULL,
			config_encrypted       JSONB NOT NULL DEFAULT '{}'::jsonb,
			oauth_tokens_encrypted JSONB NOT NULL DEFAULT '{}'::jsonb,
			is_active              BOOLEAN NOT NULL DEFAULT true,
			last_sync_at           TIMESTAMPTZ,
			sync_status            TEXT,
			created_by             UUID,
			created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (tenant_id, id),
			FOREIGN KEY (tenant_id, created_by) REFERENCES users(tenant_id, id)
		);
		ALTER TABLE connector_configs ENABLE ROW LEVEL SECURITY;
		ALTER TABLE connector_configs FORCE  ROW LEVEL SECURITY;
		CREATE POLICY connector_configs_tenant_isolation ON connector_configs
			USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
		CREATE POLICY connector_configs_tenant_isolation_insert ON connector_configs
			FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

		-- Grants dms_app would have from migration 000065 / init-db.sql;
		-- NewProdPostureDB's blanket grant ran before these tables existed.
		GRANT SELECT, INSERT, UPDATE, DELETE ON
			webhook_subscriptions, webhook_deliveries, connector_configs
			TO dms_app;
	`)
	require.NoError(t, err, "base connector tables (schema of record)")

	// Apply the connector service's REAL migrations on top
	// (is_active -> active rename, events TEXT[] -> JSONB), the same
	// chain the connector runs at startup.
	require.NoError(t,
		database.RunMigrations(db.SuperDSN, "../../migrations"),
		"connector service migrations must apply cleanly")

	// --- Seed one tenant's rows, tenant-correctly (WithTenantTx on the
	// dms_app pool — the pattern the repository SHOULD be using).
	tenant := uuid.Must(uuid.NewV7())
	webhookID := uuid.Must(uuid.NewV7())
	deliveryID := uuid.Must(uuid.NewV7())

	// Org row is out-of-band ops seeding (superuser), like prod.
	_, err = db.Super.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1)`, tenant)
	require.NoError(t, err)

	require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO webhook_subscriptions (id, tenant_id, url, secret, events, active, created_at)
			VALUES ($1, $2, 'https://example.com/hook', 'hmac-secret',
			        '["dms.document.created.v1"]'::jsonb, true, now())`,
			webhookID, tenant); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO webhook_deliveries (id, subscription_id, tenant_id, event_type, event_id, payload, status, attempts)
			VALUES ($1, $2, $3, 'dms.document.created.v1', $4, '{"k":"v"}'::jsonb, 'pending', 0)`,
			deliveryID, webhookID, tenant, deliveryID.String()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO connector_configs (tenant_id, connector_type, display_name, is_active)
			VALUES ($1, 'sap', 'SAP ERP', true)`, tenant)
		return err
	}), "tenant-correct seeding via WithTenantTx must succeed")

	// Sanity: the rows ARE there when read the tenant-correct way, so
	// any empty read below is the repository's missing tenant context,
	// not missing data.
	var n int
	require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM webhook_subscriptions)
			     + (SELECT count(*) FROM webhook_deliveries)
			     + (SELECT count(*) FROM connector_configs)`).Scan(&n)
	}))
	require.Equal(t, 3, n, "seeded rows must be visible via WithTenantTx")

	// --- The real repository, constructed over its OWN dms_app pool
	// exactly as cmd/server does in production (the service's pool has
	// run no tenant tx when the background scans and first API reads
	// fire). This also keeps the failure mode deterministic: on a fresh
	// connection app.current_tenant is undefined -> the policy's
	// current_setting(..., true) is NULL -> FORCE RLS fails closed to
	// 0 rows. (On a pooled connection that previously ran a tenant tx
	// the GUC exists as '' and the ::uuid cast errors instead — also
	// fail-closed, but not the mode this test pins.)
	repoPool, err := database.NewPool(ctx, db.AppDSN, database.DefaultPoolConfig())
	require.NoError(t, err, "dms_app pool for the repository (posture gate armed)")
	t.Cleanup(repoPool.Close)
	repo := repository.New(repoPool)

	// ListWebhooks: tenant-scoped API read (WHERE tenant_id=$1 is not
	// enough under FORCE RLS — the policy needs app.current_tenant).
	webhooks, err := repo.ListWebhooks(ctx, tenant.String())
	require.NoError(t, err)
	if assert.Len(t, webhooks, 1,
		"ListWebhooks must return the tenant's seeded subscription "+
			"(RLS fail-closed: raw pool has no app.current_tenant)") {
		assert.Equal(t, webhookID.String(), webhooks[0].ID)
	}

	// GetConnector: config read backing every sync/OAuth flow.
	cc, err := repo.GetConnector(ctx, tenant.String(), "sap")
	require.NoError(t, err)
	assert.NotNil(t, cc,
		"GetConnector must return the tenant's seeded config "+
			"(RLS fail-closed: raw pool has no app.current_tenant)")

	// ListPendingDeliveries (~repository.go:214): the background
	// delivery scan — no tenant argument at all, so under NOBYPASSRLS
	// every tenant's pending deliveries vanish and webhooks never fire.
	pending, err := repo.ListPendingDeliveries(ctx, 10)
	require.NoError(t, err)
	if assert.Len(t, pending, 1,
		"ListPendingDeliveries must see the pending delivery "+
			"(RLS fail-closed: background scan runs without tenant context)") {
		assert.Equal(t, deliveryID.String(), pending[0].ID)
	}
}
