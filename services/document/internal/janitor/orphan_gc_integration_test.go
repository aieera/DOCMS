//go:build integration

// D-25: the orphan GC soft-deleted EVERY version-less document after 24 h,
// including the ones that are metadata-only on purpose: notes / wikis (their
// content arrives later through the collaborative editor) and m365 ingests
// filed while the storage pipeline is not wired (pending=true). It now reaps
// only plain file documents without the reserved _sedoc.source marker, which
// no user-facing metadata write can set (a user-writable key like
// email.source is no exemption).
package janitor

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

// gcFixture is a fully migrated database with one tenant, workspace and
// folder; doc inserts a version-less document created 48 h ago.
type gcFixture struct {
	ctx    context.Context
	pool   *pgxpool.Pool
	tenant uuid.UUID
	ws     uuid.UUID
	folder uuid.UUID
	user   uuid.UUID
}

func newGCFixture(t *testing.T) *gcFixture {
	t.Helper()
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

	f := &gcFixture{ctx: ctx, pool: pool, tenant: uuid.Must(uuid.NewV7()), user: uuid.Must(uuid.NewV7()),
		ws: uuid.Must(uuid.NewV7()), folder: uuid.Must(uuid.NewV7())}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, 'T', $2)`, []any{f.tenant, "t-" + f.tenant.String()[:8]}},
		{`INSERT INTO users (tenant_id, id, email, display_name, role) VALUES ($1, $2, $3, 'U', 'admin')`,
			[]any{f.tenant, f.user, "u-" + f.user.String() + "@test.local"}},
		{`INSERT INTO workspaces (tenant_id, id, name) VALUES ($1, $2, 'ws')`, []any{f.tenant, f.ws}},
		{`INSERT INTO folders (tenant_id, id, workspace_id, name, path, depth) VALUES ($1, $2, $3, 'root', 'root', 0)`,
			[]any{f.tenant, f.folder, f.ws}},
	} {
		_, err := pool.Exec(ctx, q.sql, q.args...)
		require.NoError(t, err)
	}
	return f
}

func (f *gcFixture) doc(t *testing.T, docType, meta string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	_, err := f.pool.Exec(f.ctx, `
		INSERT INTO documents (tenant_id, id, workspace_id, folder_id, title, lifecycle_state, doc_type,
		                       custom_metadata, created_by, updated_by, created_at)
		VALUES ($1, $2, $3, $4, $5, 'draft', $6, $7::jsonb, $8, $8, now() - interval '48 hours')`,
		f.tenant, id, f.ws, f.folder, "d-"+id.String()[:8], docType, meta, f.user)
	require.NoError(t, err)
	return id
}

func (f *gcFixture) deleted(t *testing.T, id uuid.UUID) bool {
	t.Helper()
	var gone bool
	require.NoError(t, f.pool.QueryRow(f.ctx,
		`SELECT deleted_at IS NOT NULL FROM documents WHERE tenant_id = $1 AND id = $2`, f.tenant, id).Scan(&gone))
	return gone
}

func TestOrphanGC_SkipsIntentionalMetadataOnly(t *testing.T) {
	f := newGCFixture(t)
	ctx, pool := f.ctx, f.pool
	doc := func(docType, meta string) uuid.UUID { return f.doc(t, docType, meta) }
	abandoned := doc("file", `{}`)
	note := doc("note", `{}`)
	wiki := doc("wiki", `{}`)
	m365 := doc("file", `{"_sedoc.source":"m365","email.source":"m365_outlook_addin","email.subject":"Q3"}`)
	// A user-writable key is no exemption: email.source alone is spoofable.
	spoofed := doc("file", `{"email.source":"m365_outlook_addin"}`)

	New(pool, zerolog.Nop()).sweep(ctx)

	deleted := func(id uuid.UUID) bool { return f.deleted(t, id) }
	require.True(t, deleted(abandoned), "an abandoned upload is still reaped")
	require.False(t, deleted(note), "a note's content arrives later")
	require.False(t, deleted(wiki), "a wiki page's content arrives later")
	require.False(t, deleted(m365), "an m365 ingest filed without storage is metadata-only by design")
	require.True(t, deleted(spoofed), "only the reserved _sedoc.source marker exempts a document")
}
