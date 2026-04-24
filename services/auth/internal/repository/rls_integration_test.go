//go:build integration

package repository_test

// Wave 15.3 — cross-tenant RLS guard for the password-lifecycle
// tables. Hard-stop P0 per the Wave 15 brief: if tenant A's
// password_history or must_change_password flag bleeds into tenant
// B, the entire wave ships stopped.
//
// Runs only with `-tags integration` + either env-backed services
// (CI) or testcontainers (dev box). See pkg/testharness for the
// bootstrap options.

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/vaultdms/vaultdms/pkg/testharness"
)

func TestPasswordHistory_CrossTenantIsolation(t *testing.T) {
	h := testharness.NewWithContainers(t,
		testharness.ContainerOptions{Postgres: true},
		testharness.BootPostgres, nil, nil,
	)
	// Apply every migration the auth path cares about. The document
	// service owns the bulk `users` table; the auth service owns
	// the password-lifecycle add-on.
	h.RunMigrations(t, "../../../../services/document/migrations")
	h.RunMigrations(t, "../../../../services/auth/migrations")

	tenantA := h.SeedTenant(t, "A")
	tenantB := h.SeedTenant(t, "B")
	userA := seedUser(t, h, tenantA, "a@example.com")
	userB := seedUser(t, h, tenantB, "b@example.com")

	// Insert one history row per tenant.
	h.AssertRLSIsolated(t, tenantA, tenantB, func(tx pgx.Tx) error {
		// Each tenant's tenantID is current_setting('app.current_tenant');
		// pick the user that matches — otherwise RLS rejects the insert.
		var uid uuid.UUID
		if err := tx.QueryRow(context.Background(),
			`SELECT id FROM users WHERE tenant_id = current_setting('app.current_tenant')::uuid LIMIT 1`,
		).Scan(&uid); err != nil {
			return err
		}
		_, err := tx.Exec(context.Background(), `
			INSERT INTO password_history (tenant_id, user_id, password_hash)
			VALUES (current_setting('app.current_tenant')::uuid, $1, 'bcrypt$dummy')`, uid)
		return err
	}, func(tx pgx.Tx) (int, error) {
		var n int
		err := tx.QueryRow(context.Background(),
			`SELECT count(*) FROM password_history`,
		).Scan(&n)
		return n, err
	})

	// Sanity: the seeds shouldn't be referenced elsewhere once
	// this test exits — the harness tears down the whole DB.
	_ = userA
	_ = userB
}

func TestMustChangePassword_CrossTenantIsolation(t *testing.T) {
	h := testharness.NewWithContainers(t,
		testharness.ContainerOptions{Postgres: true},
		testharness.BootPostgres, nil, nil,
	)
	h.RunMigrations(t, "../../../../services/document/migrations")
	h.RunMigrations(t, "../../../../services/auth/migrations")

	tenantA := h.SeedTenant(t, "A")
	tenantB := h.SeedTenant(t, "B")
	userA := seedUser(t, h, tenantA, "admin-a@example.com")
	userB := seedUser(t, h, tenantB, "admin-b@example.com")

	// Flip A's must_change_password flag on. B must not observe it.
	require.NoError(t, h.WithTenantTx(tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(h.Ctx,
			`UPDATE users SET must_change_password = true WHERE id = $1`, userA)
		return err
	}))

	require.NoError(t, h.WithTenantTx(tenantB, func(tx pgx.Tx) error {
		var flagged bool
		// Tenant B sees only tenant-B users; must_change_password on
		// B's admin is still false.
		err := tx.QueryRow(h.Ctx,
			`SELECT must_change_password FROM users WHERE id = $1`, userB,
		).Scan(&flagged)
		require.NoError(t, err)
		require.False(t, flagged, "tenant B's user must not be flagged by a tenant-A write")

		// Tenant B cannot even see tenant A's row — RLS hides it.
		var n int
		err = tx.QueryRow(h.Ctx,
			`SELECT count(*) FROM users WHERE id = $1`, userA,
		).Scan(&n)
		require.NoError(t, err)
		require.Zero(t, n, "tenant B sees tenant A user through RLS")
		return nil
	}))
}

// seedUser inserts a minimal user row under the supplied tenant.
func seedUser(t *testing.T, h *testharness.Harness, tenantID uuid.UUID, email string) uuid.UUID {
	t.Helper()
	id, _ := uuid.NewV7()
	require.NoError(t, h.WithTenantTx(tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(h.Ctx, `
			INSERT INTO users (tenant_id, id, email, display_name, role, status)
			VALUES ($1, $2, $3, 'Test', 'admin', 'active')`,
			tenantID, id, email)
		return err
	}))
	return id
}
