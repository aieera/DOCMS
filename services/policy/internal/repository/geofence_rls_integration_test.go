//go:build integration

package repository_test

// Wave 15.2 — cross-tenant RLS guard for geofence_policies.
// Matches the brief's DoD #3 ("policy on tenant A does NOT affect
// tenant B") at the schema level.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/vaultdms/vaultdms/pkg/testharness"
)

func TestGeofencePolicies_CrossTenantIsolation(t *testing.T) {
	h := testharness.NewWithContainers(t,
		testharness.ContainerOptions{Postgres: true},
		testharness.BootPostgres, nil, nil,
	)
	h.RunMigrations(t, "../../../../services/document/migrations")
	h.RunMigrations(t, "../../../../services/policy/migrations")

	tenantA := h.SeedTenant(t, "A")
	tenantB := h.SeedTenant(t, "B")

	h.AssertRLSIsolated(t, tenantA, tenantB, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO geofence_policies (
				tenant_id, scope, mode, country_codes, apply_to
			) VALUES (
				current_setting('app.current_tenant')::uuid,
				'tenant', 'deny', ARRAY['CN'], '*'
			)`)
		return err
	}, func(tx pgx.Tx) (int, error) {
		var n int
		err := tx.QueryRow(context.Background(),
			`SELECT count(*) FROM geofence_policies`,
		).Scan(&n)
		return n, err
	})
}
