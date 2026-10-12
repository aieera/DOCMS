//go:build integration

// D-25: the orphan GC soft-deleted EVERY version-less document after 24 h,
// including the ones that are metadata-only on purpose: notes / wikis (their
// content arrives later through the collaborative editor) and m365 ingests
// filed while the storage pipeline is not wired (pending=true). It now reaps
// only plain file documents without such a marker.
package janitor

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

func TestOrphanGC_SkipsIntentionalMetadataOnly(t *testing.T) {
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

	tenant, user, ws, folder := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, 'T', $2)`, []any{tenant, "t-" + tenant.String()[:8]}},
		{`INSERT INTO users (tenant_id, id, email, display_name, role) VALUES ($1, $2, $3, 'U', 'admin')`,
			[]any{tenant, user, "u-" + user.String()[:8] + "@test.local"}},
		{`INSERT INTO workspaces (tenant_id, id, name) VALUES ($1, $2, 'ws')`, []any{tenant, ws}},
		{`INSERT INTO folders (tenant_id, id, workspace_id, name, path, depth) VALUES ($1, $2, $3, 'root', 'root', 0)`,
			[]any{tenant, folder, ws}},
	} {
		_, err := pool.Exec(ctx, q.sql, q.args...)
		require.NoError(t, err)
	}
	doc := func(docType, meta string) uuid.UUID {
		id := uuid.Must(uuid.NewV7())
		_, err := pool.Exec(ctx, `
			INSERT INTO documents (tenant_id, id, workspace_id, folder_id, title, lifecycle_state, doc_type,
			                       custom_metadata, created_by, updated_by, created_at)
			VALUES ($1, $2, $3, $4, $5, 'draft', $6, $7::jsonb, $8, $8, now() - interval '48 hours')`,
			tenant, id, ws, folder, "d-"+id.String()[:8], docType, meta, user)
		require.NoError(t, err)
		return id
	}
	abandoned := doc("file", `{}`)
	note := doc("note", `{}`)
	wiki := doc("wiki", `{}`)
	m365 := doc("file", `{"email.source":"m365_outlook_addin","email.subject":"Q3"}`)

	New(pool, zerolog.Nop()).sweep(ctx)

	deleted := func(id uuid.UUID) bool {
		var gone bool
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT deleted_at IS NOT NULL FROM documents WHERE tenant_id = $1 AND id = $2`, tenant, id).Scan(&gone))
		return gone
	}
	require.True(t, deleted(abandoned), "an abandoned upload is still reaped")
	require.False(t, deleted(note), "a note's content arrives later")
	require.False(t, deleted(wiki), "a wiki page's content arrives later")
	require.False(t, deleted(m365), "an m365 ingest filed without storage is metadata-only by design")
}
