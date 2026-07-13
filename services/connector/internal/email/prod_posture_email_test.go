//go:build integration && prodposture
// +build integration,prodposture

// Wave A.1 (issue #71) — email-ingestion pickup under prod NOBYPASSRLS.
// The dispatcher's dueConfigs was a global cross-tenant scan on the raw
// pool: under FORCE RLS it returned 0 rows and email ingestion silently
// stopped for every tenant. This drives the REAL tick() against a
// dms_app database with two tenants and asserts every tenant's due
// config is picked up and stamped under its own tenant context.
//
// In-package so it can drive tick() directly. The Service has no
// pollers registered, so each picked-up config takes the
// "unsupported source" recordError path — which is itself one of the
// remediated raw-pool writes; the last_run_at/last_error stamps ARE the
// proof of per-tenant pickup.
package email

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

func TestProdPosture_ConnectorEmailPickup(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)

	db := testutil.NewProdPostureDB(ctx, t, "")
	// Minimal shape of document 000041's email_ingestion_configs:
	// exactly the columns dueConfigs/recordError/recordSuccess touch,
	// with the load-bearing FORCE RLS + tenant policy.
	_, err := db.Super.Exec(ctx, `
		CREATE TABLE organizations (
			id         UUID PRIMARY KEY,
			deleted_at TIMESTAMPTZ
		);
		CREATE TABLE email_ingestion_configs (
			tenant_id             UUID NOT NULL REFERENCES organizations(id),
			id                    UUID NOT NULL DEFAULT gen_random_uuid(),
			source                TEXT NOT NULL,
			label                 TEXT NOT NULL,
			active                BOOLEAN NOT NULL DEFAULT TRUE,
			oauth_provider        TEXT,
			imap_host             TEXT,
			imap_port             INT,
			imap_use_tls          BOOLEAN NOT NULL DEFAULT TRUE,
			imap_username         TEXT,
			imap_password_encrypted BYTEA,
			target_workspace_id   UUID,
			target_folder_id      UUID,
			poll_interval_seconds INT NOT NULL DEFAULT 300,
			last_run_at           TIMESTAMPTZ,
			last_success_at       TIMESTAMPTZ,
			last_error            TEXT,
			messages_ingested     BIGINT NOT NULL DEFAULT 0,
			created_by            UUID,
			created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (tenant_id, id)
		);
		ALTER TABLE email_ingestion_configs ENABLE ROW LEVEL SECURITY;
		ALTER TABLE email_ingestion_configs FORCE  ROW LEVEL SECURITY;
		CREATE POLICY eic_tenant_isolation ON email_ingestion_configs
			USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
			WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
		GRANT SELECT, INSERT, UPDATE, DELETE ON organizations, email_ingestion_configs TO dms_app;
	`)
	require.NoError(t, err)

	tenantA := uuid.Must(uuid.NewV7())
	tenantB := uuid.Must(uuid.NewV7())
	_, err = db.Super.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1), ($2)`, tenantA, tenantB)
	require.NoError(t, err)

	seed := func(tenant uuid.UUID) uuid.UUID {
		id := uuid.Must(uuid.NewV7())
		require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
				INSERT INTO email_ingestion_configs (tenant_id, id, source, label, active, poll_interval_seconds)
				VALUES ($1, $2, 'imap', 'inbox', TRUE, 60)`, tenant, id)
			return err
		}))
		return id
	}
	cfgA := seed(tenantA)
	cfgB := seed(tenantB)

	// The REAL dispatcher over the dms_app pool, no pollers registered.
	svc := New(db.App, nil, nil, nil, zerolog.Nop())
	svc.tick(ctx)

	// Both tenants' configs must have been picked up (enumerated per
	// tenant) and stamped under their own context.
	assertStamped := func(tenant, id uuid.UUID) {
		var lastRun *time.Time
		var lastErr *string
		require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`SELECT last_run_at, last_error FROM email_ingestion_configs WHERE id = $1`, id).
				Scan(&lastRun, &lastErr)
		}))
		require.NotNil(t, lastRun,
			"config must be picked up and stamped under NOBYPASSRLS (per-tenant enumeration)")
		require.NotNil(t, lastErr)
		require.Contains(t, *lastErr, "unsupported source",
			"the no-poller path proves the run reached the tenant-scoped recordError")
	}
	assertStamped(tenantA, cfgA)
	assertStamped(tenantB, cfgB)
}
