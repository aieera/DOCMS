//go:build integration && prodposture
// +build integration,prodposture

// Wave A.1 (issue #71) — intake drop-folder enumeration under prod
// NOBYPASSRLS. The reconcile loop's listActiveFolders was a global
// cross-tenant scan on the raw pool: under FORCE RLS it returned 0 rows
// and intake silently stopped watching every tenant's folders. This
// pins the per-tenant enumeration shape.
//
// In-package so it can call listActiveFolders directly (the reconcile
// loop's DB phase) without filesystem watchers.
package intake

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

func TestProdPosture_ConnectorIntakeEnumeration(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)

	db := testutil.NewProdPostureDB(ctx, t, "")
	// Minimal shape of document 000042's intake_drop_folders: the
	// columns listActiveFolders/recordError touch + FORCE RLS.
	_, err := db.Super.Exec(ctx, `
		CREATE TABLE organizations (
			id         UUID PRIMARY KEY,
			deleted_at TIMESTAMPTZ
		);
		CREATE TABLE intake_drop_folders (
			tenant_id           UUID NOT NULL REFERENCES organizations(id),
			id                  UUID NOT NULL DEFAULT gen_random_uuid(),
			label               TEXT NOT NULL,
			active              BOOLEAN NOT NULL DEFAULT TRUE,
			host_path           TEXT NOT NULL,
			target_workspace_id UUID,
			target_folder_id    UUID,
			quarantine_subdir   TEXT NOT NULL DEFAULT '.quarantine',
			processed_subdir    TEXT NOT NULL DEFAULT '.processed',
			recurse             BOOLEAN NOT NULL DEFAULT FALSE,
			extensions_csv      TEXT NOT NULL DEFAULT '',
			last_seen_at        TIMESTAMPTZ,
			files_ingested      BIGINT NOT NULL DEFAULT 0,
			last_error          TEXT,
			created_by          UUID,
			created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (tenant_id, id)
		);
		ALTER TABLE intake_drop_folders ENABLE ROW LEVEL SECURITY;
		ALTER TABLE intake_drop_folders FORCE  ROW LEVEL SECURITY;
		CREATE POLICY idf_tenant_isolation ON intake_drop_folders
			USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
			WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
		GRANT SELECT, INSERT, UPDATE, DELETE ON organizations, intake_drop_folders TO dms_app;
	`)
	require.NoError(t, err)

	tenantA := uuid.Must(uuid.NewV7())
	tenantB := uuid.Must(uuid.NewV7())
	_, err = db.Super.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1), ($2)`, tenantA, tenantB)
	require.NoError(t, err)

	seed := func(tenant uuid.UUID, label string) {
		require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
				INSERT INTO intake_drop_folders (tenant_id, label, active, host_path)
				VALUES ($1, $2, TRUE, '/tmp/'||$2)`, tenant, label)
			return err
		}))
	}
	seed(tenantA, "drop-a")
	seed(tenantB, "drop-b")
	// An inactive folder must not be enumerated.
	require.NoError(t, database.WithTenantTx(ctx, db.App, tenantB, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO intake_drop_folders (tenant_id, label, active, host_path)
			VALUES ($1, 'drop-b-off', FALSE, '/tmp/off')`, tenantB)
		return err
	}))

	svc := New(db.App, nil, zerolog.Nop())
	folders, err := svc.listActiveFolders(ctx)
	require.NoError(t, err)
	require.Len(t, folders, 2,
		"the reconcile scan must see every tenant's active folders under NOBYPASSRLS")
	byTenant := map[string]string{}
	for _, f := range folders {
		byTenant[f.TenantID] = f.Label
	}
	require.Equal(t, "drop-a", byTenant[tenantA.String()])
	require.Equal(t, "drop-b", byTenant[tenantB.String()])
}
