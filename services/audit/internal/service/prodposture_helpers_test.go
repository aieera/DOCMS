//go:build integration && prodposture

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/testutil"
)

// newProdPostureAuditDB is the audit schema under the prod role split: the
// superuser runs the migrations with the same default privileges
// scripts/init-db.sql gives the migration role (every new table grants
// dms_app SELECT/INSERT/UPDATE/DELETE), and the App pool connects as
// dms_app NOBYPASSRLS with the posture gate armed. Grants are whatever the
// migrations leave — nothing is re-granted afterwards.
func newProdPostureAuditDB(ctx context.Context, t *testing.T) *testutil.ProdPostureDB {
	t.Helper()
	db := testutil.NewProdPostureDB(ctx, t, "")
	_, err := db.Super.Exec(ctx, `
		CREATE ROLE dms_readonly NOLOGIN;
		ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO dms_app;
		ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO dms_app;
		ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO dms_readonly;
	`)
	require.NoError(t, err, "init-db default privileges")
	migrateAuditSchema(t, db.SuperDSN)
	return db
}
