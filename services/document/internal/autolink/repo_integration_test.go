//go:build integration

// D-19: autolink built edges tenant-wide from user-set metadata, so anyone
// able to set custom_metadata in their own workspace could link their
// document to (and surface in the graph) documents of any other workspace
// that happened to share a number. FindTargets now matches only inside the
// source document's workspace.
package autolink_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/document/internal/autolink"
)

func TestFindTargets_ScopedToSourceWorkspace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	dsn, pgClean, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(pgClean)
	require.NoError(t, database.RunMigrations(dsn, "../../migrations"))
	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	tenant, user := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	_, err = pool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, 'T', $2)`, tenant, "t-"+tenant.String()[:8])
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO users (tenant_id, id, email, display_name, role) VALUES ($1, $2, $3, 'U', 'admin')`,
		tenant, user, "u-"+user.String()[:8]+"@test.local")
	require.NoError(t, err)
	type wsf struct{ ws, folder uuid.UUID }
	mkWS := func() wsf {
		w := wsf{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}
		_, err := pool.Exec(ctx, `INSERT INTO workspaces (tenant_id, id, name) VALUES ($1, $2, $3)`, tenant, w.ws, "ws-"+w.ws.String()[:8])
		require.NoError(t, err)
		_, err = pool.Exec(ctx, `INSERT INTO folders (tenant_id, id, workspace_id, name, path, depth) VALUES ($1, $2, $3, 'root', 'root', 0)`,
			tenant, w.folder, w.ws)
		require.NoError(t, err)
		return w
	}
	doc := func(w wsf, meta string) uuid.UUID {
		id := uuid.Must(uuid.NewV7())
		_, err := pool.Exec(ctx, `
			INSERT INTO documents (tenant_id, id, workspace_id, folder_id, title, lifecycle_state, created_by, updated_by, custom_metadata)
			VALUES ($1, $2, $3, $4, $5, 'active', $6, $6, $7::jsonb)`, tenant, id, w.ws, w.folder, "d-"+id.String()[:8], user, meta)
		require.NoError(t, err)
		return id
	}

	mine, theirs := mkWS(), mkWS()
	src := doc(mine, `{"reference_number":"PO-77"}`)
	sameWS := doc(mine, `{"reference_number":"PO-77"}`)
	otherWS := doc(theirs, `{"reference_number":"PO-77"}`)

	store := autolink.NewStore(pool)
	got, err := store.FindTargets(ctx, tenant, autolink.Rule{Kind: "number", Key: "reference_number", Value: "PO-77"}, src)
	require.NoError(t, err)
	require.Contains(t, got, sameWS)
	require.NotContains(t, got, otherWS, "a shared number must not link across workspaces")
}
