//go:build integration

// D-25 — migration 000114 backfills the reserved _sedoc.source marker on
// documents ingested from Outlook before the marker existed (they carry only
// the user-writable 'email.source'), so the orphan GC — which now exempts
// only _sedoc.source — does not reap legacy metadata-only m365 ingests after
// the deploy. Down removes only that key.
//
// Like DOC3's 000113 test, the pre-change row is seeded on the fully migrated
// database and the migration body is executed directly, so the test does not
// depend on rolling back other workstreams' later migrations.
package janitor

import (
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestMigration000114_BackfillsSedocSource(t *testing.T) {
	f := newGCFixture(t)
	run := func(file string) {
		t.Helper()
		sql, err := os.ReadFile("../../migrations/" + file)
		require.NoError(t, err)
		_, err = f.pool.Exec(f.ctx, string(sql))
		require.NoError(t, err)
	}
	meta := func(id uuid.UUID) map[string]any {
		t.Helper()
		var m map[string]any
		require.NoError(t, f.pool.QueryRow(f.ctx,
			`SELECT custom_metadata FROM documents WHERE tenant_id = $1 AND id = $2`, f.tenant, id).Scan(&m))
		return m
	}

	// Pre-change m365 ingests: only email.source (one of them filed by the
	// new code already, so it is marked).
	legacy := f.doc(t, "file", `{"email.source":"m365_outlook_addin","email.subject":"Q3"}`)
	alreadyMarked := f.doc(t, "file", `{"email.source":"m365_outlook_addin","_sedoc.source":"m365"}`)
	plain := f.doc(t, "file", `{"matter":"x"}`)

	run("000114_sedoc_source_backfill.up.sql")
	run("000114_sedoc_source_backfill.up.sql") // idempotent

	require.Equal(t, "m365", meta(legacy)["_sedoc.source"])
	require.Equal(t, "Q3", meta(legacy)["email.subject"], "other keys untouched")
	require.Equal(t, "m365", meta(alreadyMarked)["_sedoc.source"])
	require.NotContains(t, meta(plain), "_sedoc.source")

	New(f.pool, zerolog.Nop()).sweep(f.ctx)
	require.False(t, f.deleted(t, legacy), "a backfilled legacy m365 ingest is exempt from the GC")
	require.True(t, f.deleted(t, plain), "an abandoned upload is still reaped")

	run("000114_sedoc_source_backfill.down.sql")
	require.NotContains(t, meta(legacy), "_sedoc.source", "down removes the key")
	require.Equal(t, "m365_outlook_addin", meta(legacy)["email.source"], "and only that key")
	require.Equal(t, "Q3", meta(legacy)["email.subject"])
}
