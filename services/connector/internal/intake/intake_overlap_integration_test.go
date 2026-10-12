//go:build integration && prodposture
// +build integration,prodposture

package intake

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

// N-6: no two watched folders may overlap (same path, or one inside the
// other) — two watchers would race on the same files and rename each
// other's processed/quarantine trees. Across tenants overlap is impossible
// by confinement (<root>/<tenant_id>); within a tenant CreateFolder checks
// every existing row (active or not) under an advisory lock.
func TestIntakeCreate_OverlappingPathRejected(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	fx := newIntakeFixture(ctx, t, &scriptedIngester{})
	tenant := fx.tenant.String()
	troot := filepath.Join(fx.svc.root, tenant)
	actor := uuid.NewString()

	for _, p := range []string{
		fx.inbox,                       // identical
		filepath.Join(fx.inbox, "sub"), // inside an existing folder
		filepath.Join(fx.inbox, "processed"),
		troot,          // contains an existing folder
		fx.inbox + "/", // same after cleaning
	} {
		_, err := fx.svc.CreateFolder(ctx, tenant, actor, CreateInput{Label: "x", HostPath: p})
		require.ErrorIs(t, err, ErrHostPathOverlap, "host_path %q overlaps %q", p, fx.inbox)
	}

	// Shares a string prefix but not a path prefix: allowed.
	sibling := fx.inbox + "2"
	f, err := fx.svc.CreateFolder(ctx, tenant, actor, CreateInput{Label: "sibling", HostPath: sibling})
	require.NoError(t, err)
	require.Equal(t, sibling, f.HostPath)

	// Another tenant can never reach this tenant's paths (confinement), and
	// its own root is independent.
	other := uuid.New()
	_, err = fx.db.Super.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1)`, other)
	require.NoError(t, err)
	_, err = fx.svc.CreateFolder(ctx, other.String(), actor, CreateInput{Label: "steal", HostPath: fx.inbox})
	require.ErrorIs(t, err, ErrHostPathNotConfined)
	otherInbox := filepath.Join(fx.svc.root, other.String(), "inbox")
	require.NoError(t, os.MkdirAll(otherInbox, 0o755))
	_, err = fx.svc.CreateFolder(ctx, other.String(), actor, CreateInput{Label: "own", HostPath: otherInbox})
	require.NoError(t, err)
}

// N-6 follow-up: an overlap reached through a symlink inside the tenant
// root is still an overlap, and connector migration 000002 adds a global
// host_path unique index as defence in depth.
func TestIntakeCreate_SymlinkOverlapRejected(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	fx := newIntakeFixture(ctx, t, &scriptedIngester{})
	tenant := fx.tenant.String()
	troot := filepath.Join(fx.svc.root, tenant)

	alias := filepath.Join(troot, "alias")
	require.NoError(t, os.Symlink(fx.inbox, alias))
	_, err := fx.svc.CreateFolder(ctx, tenant, uuid.NewString(), CreateInput{Label: "via-link", HostPath: filepath.Join(alias, "sub")})
	require.ErrorIs(t, err, ErrHostPathOverlap, "alias/sub resolves inside the existing folder")
	_, err = fx.svc.CreateFolder(ctx, tenant, uuid.NewString(), CreateInput{Label: "via-link", HostPath: alias})
	require.ErrorIs(t, err, ErrHostPathOverlap, "alias resolves to the existing folder")

	require.NoError(t, database.RunMigrations(fx.db.SuperDSN, "../../migrations"))
	other := uuid.New()
	_, err = fx.db.Super.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1)`, other)
	require.NoError(t, err)
	_, err = fx.db.Super.Exec(ctx, `INSERT INTO intake_drop_folders (tenant_id, label, host_path) VALUES ($1, 'dup', $2)`, other, fx.inbox)
	require.Error(t, err, "host_path must be globally unique across tenants")
}

// Pre-existing cross-tenant duplicates must not fail the deploy: 000002
// warns and skips the index.
func TestMigration000002_DuplicateHostPathsDoNotFail(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	fx := newIntakeFixture(ctx, t, &scriptedIngester{})
	other := uuid.New()
	_, err := fx.db.Super.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1)`, other)
	require.NoError(t, err)
	_, err = fx.db.Super.Exec(ctx, `INSERT INTO intake_drop_folders (tenant_id, label, host_path) VALUES ($1, 'dup', $2)`, other, fx.inbox)
	require.NoError(t, err)
	require.NoError(t, database.RunMigrations(fx.db.SuperDSN, "../../migrations"))
	var idx *string
	require.NoError(t, fx.db.Super.QueryRow(ctx, `SELECT to_regclass('public.uq_intake_drop_folders_host_path')::text`).Scan(&idx))
	require.Nil(t, idx, "index must be skipped while duplicates exist")
}
