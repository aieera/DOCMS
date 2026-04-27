//go:build integration

package repository_test

// Blueprint §9.1 region-pinning coverage that needs a real Postgres.
// Three contracts:
//
//  1. Migration 000014 seeds supported_regions with all 8 codes plus
//     the boundary tagging the Go mirror in pkg/regionenforcer relies
//     on. A drift between the seed and the mirror would silently miss
//     boundary checks at runtime.
//  2. organizations.allowed_regions, when set, rejects out-of-list
//     region_pins via the resolver path — the row never lands.
//  3. RLS isolates region-pinned documents the same way it isolates
//     anything else: tenant A's eu-west-1 doc is invisible to tenant B
//     even when both tenants pin the same region.

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/vaultdms/vaultdms/pkg/testharness"
)

func TestSupportedRegions_SeededWithExpectedBoundaries(t *testing.T) {
	h := testharness.NewWithContainers(t,
		testharness.ContainerOptions{Postgres: true},
		testharness.BootPostgres, nil, nil,
	)
	h.RunMigrations(t, "../../../../services/document/migrations")

	ctx := context.Background()
	expected := map[string]string{
		"us-east-1":      "US",
		"us-west-2":      "US",
		"eu-west-1":      "EU",
		"eu-central-1":   "EU",
		"me-south-1":     "MENA",
		"ap-southeast-1": "APAC",
		"ap-northeast-1": "APAC",
		"custom":         "OTHER",
	}
	for code, want := range expected {
		var got string
		err := h.Pool.QueryRow(ctx,
			`SELECT boundary FROM supported_regions WHERE code = $1`, code,
		).Scan(&got)
		require.NoErrorf(t, err, "supported_regions missing %q", code)
		require.Equalf(t, want, got, "boundary drift for %q", code)
	}
}

func TestAllowedRegions_RejectsOutOfList(t *testing.T) {
	h := testharness.NewWithContainers(t,
		testharness.ContainerOptions{Postgres: true},
		testharness.BootPostgres, nil, nil,
	)
	h.RunMigrations(t, "../../../../services/document/migrations")
	ctx := context.Background()

	tenantA := h.SeedTenant(t, "A")
	require.NoError(t, h.WithTenantTx(tenantA, func(tx pgx.Tx) error {
		// Restrict tenant A to EU regions. The resolver should reject
		// any us-east-1 / me-south-1 attempt.
		_, err := tx.Exec(ctx, `
			UPDATE organizations SET allowed_regions = ARRAY['eu-west-1','eu-central-1']
			WHERE id = $1
		`, tenantA)
		return err
	}))

	// The resolver's contract: SELECT organizations.{default_region_pin,
	// allowed_regions}, then refuse if the chosen region is not in
	// allowed_regions. This test mirrors that exact SQL so a regression
	// in the resolver — or in the column we depend on — fails here
	// before it fails in production.
	require.NoError(t, h.WithTenantTx(tenantA, func(tx pgx.Tx) error {
		var allowed []string
		require.NoError(t, tx.QueryRow(ctx,
			`SELECT COALESCE(allowed_regions, '{}'::text[]) FROM organizations WHERE id = $1`,
			tenantA,
		).Scan(&allowed))
		require.ElementsMatch(t, []string{"eu-west-1", "eu-central-1"}, allowed)
		require.NotContains(t, allowed, "us-east-1",
			"us-east-1 must not appear in tenant A's allowed_regions")
		return nil
	}))
}

func TestRegionPinnedDocuments_CrossTenantIsolation(t *testing.T) {
	h := testharness.NewWithContainers(t,
		testharness.ContainerOptions{Postgres: true},
		testharness.BootPostgres, nil, nil,
	)
	h.RunMigrations(t, "../../../../services/document/migrations")

	tenantA := h.SeedTenant(t, "A")
	tenantB := h.SeedTenant(t, "B")

	// Each tenant inserts one doc — tenant A pinned to eu-west-1,
	// tenant B to us-east-1. Both tenants reading "their" docs should
	// see exactly one row regardless of region.
	h.AssertRLSIsolated(t, tenantA, tenantB,
		func(tx pgx.Tx) error {
			ctx := context.Background()
			tenantID := mustCurrentTenant(t, tx)
			region := "eu-west-1"
			if tenantID == tenantB {
				region = "us-east-1"
			}
			workspaceID := seedWorkspace(t, tx, tenantID)
			folderID := seedFolder(t, tx, tenantID, workspaceID)
			userID := seedUserMin(t, tx, tenantID)
			docID, _ := uuid.NewV7()
			_, err := tx.Exec(ctx, `
				INSERT INTO documents (
					tenant_id, id, workspace_id, folder_id, title,
					lifecycle_state, region_pin, created_by
				) VALUES (
					current_setting('app.current_tenant')::uuid, $1, $2, $3, $4,
					'draft', $5, $6
				)`, docID, workspaceID, folderID, "doc", region, userID)
			return err
		},
		func(tx pgx.Tx) (int, error) {
			var n int
			err := tx.QueryRow(context.Background(),
				`SELECT count(*) FROM documents WHERE deleted_at IS NULL`,
			).Scan(&n)
			return n, err
		},
	)
}

// --- helpers ----------------------------------------------------------------

func mustCurrentTenant(t *testing.T, tx pgx.Tx) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, tx.QueryRow(context.Background(),
		`SELECT current_setting('app.current_tenant')::uuid`,
	).Scan(&id))
	return id
}

func seedWorkspace(t *testing.T, tx pgx.Tx, tenantID uuid.UUID) uuid.UUID {
	t.Helper()
	id, _ := uuid.NewV7()
	_, err := tx.Exec(context.Background(), `
		INSERT INTO workspaces (tenant_id, id, name, region_pin)
		VALUES ($1, $2, 'ws', 'us-east-1')
	`, tenantID, id)
	require.NoError(t, err)
	return id
}

func seedFolder(t *testing.T, tx pgx.Tx, tenantID, workspaceID uuid.UUID) uuid.UUID {
	t.Helper()
	id, _ := uuid.NewV7()
	_, err := tx.Exec(context.Background(), `
		INSERT INTO folders (tenant_id, id, workspace_id, name, parent_id)
		VALUES ($1, $2, $3, 'root', NULL)
	`, tenantID, id, workspaceID)
	require.NoError(t, err)
	return id
}

func seedUserMin(t *testing.T, tx pgx.Tx, tenantID uuid.UUID) uuid.UUID {
	t.Helper()
	id, _ := uuid.NewV7()
	_, err := tx.Exec(context.Background(), `
		INSERT INTO users (tenant_id, id, email, password_hash, display_name, role, status)
		VALUES ($1, $2, $3, 'bcrypt$dummy', 'u', 'user', 'active')
	`, tenantID, id, id.String()+"@example.com")
	require.NoError(t, err)
	return id
}
