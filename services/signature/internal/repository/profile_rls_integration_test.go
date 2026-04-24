//go:build integration

package repository_test

// Wave 15.4 — cross-tenant RLS guard for signature_profiles + the
// partial unique index that enforces "one default per user".
// Hard-stop P0 if either assertion fails.

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/vaultdms/vaultdms/pkg/testharness"
)

func TestSignatureProfiles_CrossTenantIsolation(t *testing.T) {
	h := testharness.NewWithContainers(t,
		testharness.ContainerOptions{Postgres: true},
		testharness.BootPostgres, nil, nil,
	)
	h.RunMigrations(t, "../../../../services/document/migrations")
	h.RunMigrations(t, "../../../../services/signature/migrations")

	tenantA := h.SeedTenant(t, "A")
	tenantB := h.SeedTenant(t, "B")

	h.AssertRLSIsolated(t, tenantA, tenantB, func(tx pgx.Tx) error {
		userID, _ := uuid.NewV7()
		_, err := tx.Exec(context.Background(), `
			INSERT INTO signature_profiles (
				tenant_id, user_id, name, kind,
				image_ref, wrapped_dek, kek_id, nonce, image_size_bytes
			) VALUES (
				current_setting('app.current_tenant')::uuid, $1, 'default', 'draw',
				'k/'||$1::text, '\x00'::bytea, 'test-kek', '\x000000000000000000000000'::bytea, 42
			)`, userID)
		return err
	}, func(tx pgx.Tx) (int, error) {
		var n int
		err := tx.QueryRow(context.Background(),
			`SELECT count(*) FROM signature_profiles`,
		).Scan(&n)
		return n, err
	})
}

func TestSignatureProfiles_DefaultUniquePerUser(t *testing.T) {
	h := testharness.NewWithContainers(t,
		testharness.ContainerOptions{Postgres: true},
		testharness.BootPostgres, nil, nil,
	)
	h.RunMigrations(t, "../../../../services/document/migrations")
	h.RunMigrations(t, "../../../../services/signature/migrations")

	tenantID := h.SeedTenant(t, "Acme")
	userID, _ := uuid.NewV7()

	// First default — accepted.
	require.NoError(t, h.WithTenantTx(tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(h.Ctx, `
			INSERT INTO signature_profiles (
				tenant_id, user_id, name, kind,
				image_ref, wrapped_dek, kek_id, nonce, image_size_bytes, is_default
			) VALUES ($1, $2, 'one', 'draw',
			          'k/1', '\x00'::bytea, 'kek', '\x000000000000000000000000'::bytea, 10, true)`,
			tenantID, userID)
		return err
	}))

	// Second default for the same user — partial unique index fires.
	err := h.WithTenantTx(tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(h.Ctx, `
			INSERT INTO signature_profiles (
				tenant_id, user_id, name, kind,
				image_ref, wrapped_dek, kek_id, nonce, image_size_bytes, is_default
			) VALUES ($1, $2, 'two', 'draw',
			          'k/2', '\x00'::bytea, 'kek', '\x000000000000000000000000'::bytea, 10, true)`,
			tenantID, userID)
		return err
	})
	require.Error(t, err, "partial unique index must reject a second is_default=true row")
}
