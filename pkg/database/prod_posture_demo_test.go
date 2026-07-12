//go:build integration && prodposture
// +build integration,prodposture

// Run with the prod-posture lane:
//
//	go test -tags "integration prodposture" -run '^TestProdPosture_' ./pkg/database/
//
// This is the lane's demonstration test (Wave A DoD): under the enforced
// NOBYPASSRLS posture, the SAME table read two ways behaves differently —
// a raw-pool query with no tenant context returns 0 rows (fail-closed),
// while WithTenantTx sees the tenant's data. It is deliberately
// self-contained (creates its own FORCE-RLS table with the canonical
// policy shape) so it stays green independent of any service's migration
// health — the document migration chain is currently broken on a clean DB
// by 000021 (STATE 2026-07-03 "Migration blocker"), which is a separate
// remediation item.
package database_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

func TestProdPosture_RawPoolFailsClosed_TenantTxSucceeds(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)

	db := testutil.NewProdPostureDB(ctx, t, "")

	// Canonical SeDoc tenant table shape: tenant_id first, ENABLE +
	// FORCE ROW LEVEL SECURITY, isolation policy on app.current_tenant
	// (same shape as e.g. services/document/migrations/000060).
	_, err := db.Super.Exec(ctx, `
		CREATE TABLE demo_secrets (
			tenant_id UUID NOT NULL,
			id        UUID NOT NULL,
			secret    TEXT NOT NULL,
			PRIMARY KEY (tenant_id, id)
		);
		ALTER TABLE demo_secrets ENABLE ROW LEVEL SECURITY;
		ALTER TABLE demo_secrets FORCE ROW LEVEL SECURITY;
		CREATE POLICY demo_secrets_isolation ON demo_secrets
			USING (tenant_id::text = current_setting('app.current_tenant', true));
		GRANT SELECT, INSERT, UPDATE, DELETE ON demo_secrets TO dms_app;
	`)
	require.NoError(t, err)

	tenantA := uuid.Must(uuid.NewV7())
	tenantB := uuid.Must(uuid.NewV7())

	// Writes go through WithTenantTx on the dms_app pool — the only
	// tenant-correct pattern (sets SET LOCAL app.current_tenant).
	seed := func(tenant uuid.UUID, secret string) {
		require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx,
				`INSERT INTO demo_secrets (tenant_id, id, secret) VALUES ($1, $2, $3)`,
				tenant, uuid.Must(uuid.NewV7()), secret)
			return err
		}))
	}
	seed(tenantA, "a-secret")
	seed(tenantB, "b-secret")

	// WithTenantTx sees exactly the tenant's row.
	var got []string
	require.NoError(t, database.WithTenantTx(ctx, db.App, tenantA, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT secret FROM demo_secrets ORDER BY secret`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return err
			}
			got = append(got, s)
		}
		return rows.Err()
	}))
	require.Equal(t, []string{"a-secret"}, got,
		"WithTenantTx must see exactly tenant A's row under FORCE RLS")

	// The same table on the raw dms_app pool — no tenant GUC — must
	// fail closed: 0 rows (the policy's current_setting is NULL). This
	// is the exact failure mode the audit's raw-pool repos hit in prod.
	//
	// Reset the pool first to pin the fresh-connection mode: on a
	// connection previously used by WithTenantTx, the committed
	// set_config leaves the custom GUC defined as '' at session level
	// (Postgres placeholder quirk), and the policy's ::uuid cast then
	// errors 22P02 instead of returning 0 rows. Both are fail-closed —
	// prod shows either depending on connection history (tracked as a
	// GUC-hygiene follow-up; see issue #77).
	db.App.Reset()
	var count int
	err = db.App.QueryRow(ctx, `SELECT COUNT(*) FROM demo_secrets`).Scan(&count)
	require.NoError(t, err)
	require.Zero(t, count,
		"raw pool query without tenant context must return 0 rows under NOBYPASSRLS")

	// And a raw write without the GUC is rejected outright.
	_, err = db.App.Exec(ctx,
		`INSERT INTO demo_secrets (tenant_id, id, secret) VALUES ($1, $2, 'x')`,
		tenantA, uuid.Must(uuid.NewV7()))
	require.Error(t, err,
		"raw insert without tenant context must violate the RLS policy")
}
