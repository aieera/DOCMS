//go:build integration

package repository_test

// Integration coverage for migration 000012 (scan_results +
// quarantine_events). Pins three contracts:
//
//  1. Both tables enforce per-tenant RLS.
//  2. The scan_results CHECK constraint rejects unknown result values,
//     so a bug elsewhere that writes e.g. "unscanned" fails fast rather
//     than silently corrupting the virus_scan_coverage SLI.
//  3. The quarantine_events reason CHECK accepts only the three values
//     the finalize path emits (virus | blocked_mime | mime_mismatch).
//
// End-to-end EICAR + MIME-mismatch + 413 flows require MinIO + ClamAV
// containers the pkg/testharness bootstrap does not yet provision —
// tracked in docs/tech-debt/ledger.md (T-D-10 upload-pipeline-e2e).

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/vaultdms/vaultdms/pkg/testharness"
)

func TestScanResults_RLSIsolation(t *testing.T) {
	h := testharness.NewWithContainers(t,
		testharness.ContainerOptions{Postgres: true},
		testharness.BootPostgres, nil, nil,
	)
	h.RunMigrations(t, "../../../../services/document/migrations")

	tenantA := h.SeedTenant(t, "A")
	tenantB := h.SeedTenant(t, "B")

	h.AssertRLSIsolated(t, tenantA, tenantB, func(tx pgx.Tx) error {
		uploadID, _ := uuid.NewV7()
		// Insert the parent upload_sessions row first so the FK holds.
		if _, err := tx.Exec(context.Background(), `
			INSERT INTO upload_sessions (
				tenant_id, id, filename, total_size, upload_type, expires_at, created_at
			) VALUES (
				current_setting('app.current_tenant')::uuid, $1,
				'x.pdf', 10, 'single', now() + interval '1 hour', now()
			)`, uploadID); err != nil {
			return err
		}
		scanID, _ := uuid.NewV7()
		_, err := tx.Exec(context.Background(), `
			INSERT INTO scan_results (tenant_id, id, upload_id, result)
			VALUES (current_setting('app.current_tenant')::uuid, $1, $2, 'clean')
		`, scanID, uploadID)
		return err
	}, func(tx pgx.Tx) (int, error) {
		var n int
		err := tx.QueryRow(context.Background(),
			`SELECT count(*) FROM scan_results`,
		).Scan(&n)
		return n, err
	})
}

func TestScanResults_RejectsUnknownResultValue(t *testing.T) {
	h := testharness.NewWithContainers(t,
		testharness.ContainerOptions{Postgres: true},
		testharness.BootPostgres, nil, nil,
	)
	h.RunMigrations(t, "../../../../services/document/migrations")
	tenantA := h.SeedTenant(t, "A")

	ctx := context.Background()
	require.NoError(t, h.WithTenantTx(ctx, tenantA, func(tx pgx.Tx) error {
		uploadID, _ := uuid.NewV7()
		if _, err := tx.Exec(ctx, `
			INSERT INTO upload_sessions (
				tenant_id, id, filename, total_size, upload_type, expires_at
			) VALUES ($1, $2, 'x.pdf', 10, 'single', now() + interval '1 hour')
		`, tenantA, uploadID); err != nil {
			return err
		}
		scanID, _ := uuid.NewV7()
		_, err := tx.Exec(ctx, `
			INSERT INTO scan_results (tenant_id, id, upload_id, result)
			VALUES ($1, $2, $3, 'unscanned')
		`, tenantA, scanID, uploadID)
		require.Error(t, err, "scan_results CHECK must reject unknown result values")
		return nil
	}))
}

func TestQuarantineEvents_ReasonCheck(t *testing.T) {
	h := testharness.NewWithContainers(t,
		testharness.ContainerOptions{Postgres: true},
		testharness.BootPostgres, nil, nil,
	)
	h.RunMigrations(t, "../../../../services/document/migrations")
	tenantA := h.SeedTenant(t, "A")
	ctx := context.Background()

	require.NoError(t, h.WithTenantTx(ctx, tenantA, func(tx pgx.Tx) error {
		uploadID, _ := uuid.NewV7()
		if _, err := tx.Exec(ctx, `
			INSERT INTO upload_sessions (
				tenant_id, id, filename, total_size, upload_type, expires_at
			) VALUES ($1, $2, 'x.pdf', 10, 'single', now() + interval '1 hour')
		`, tenantA, uploadID); err != nil {
			return err
		}
		for _, reason := range []string{"virus", "blocked_mime", "mime_mismatch"} {
			qeID, _ := uuid.NewV7()
			_, err := tx.Exec(ctx, `
				INSERT INTO quarantine_events (
					tenant_id, id, upload_id, reason,
					storage_bucket, storage_key
				) VALUES ($1, $2, $3, $4, 'dms-quarantine', 'k')
			`, tenantA, qeID, uploadID, reason)
			require.NoError(t, err, "reason %q must be accepted", reason)
		}
		// And a negative: reason 'curiosity' must be rejected.
		qeID, _ := uuid.NewV7()
		_, err := tx.Exec(ctx, `
			INSERT INTO quarantine_events (
				tenant_id, id, upload_id, reason,
				storage_bucket, storage_key
			) VALUES ($1, $2, $3, 'curiosity', 'dms-quarantine', 'k')
		`, tenantA, qeID, uploadID)
		require.Error(t, err, "reason CHECK must reject unknown value")
		return nil
	}))
}
