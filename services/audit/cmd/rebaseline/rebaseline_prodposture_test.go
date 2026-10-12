//go:build integration && prodposture

package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // migrate driver
	_ "github.com/golang-migrate/migrate/v4/source/file"       // migrate source
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

// H-12 round 3: the re-baseline rewrites WORM rows, so it must run as the
// owner / a BYPASSRLS operator role. Under the app role (dms_app,
// NOBYPASSRLS) it refuses with a non-zero exit and writes nothing — it
// must never be wired into a service.
func TestRebaseline_RefusesNonBypassRole(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	db := testutil.NewProdPostureDB(ctx, t, "")
	m, err := migrate.New("file://../../../../services/document/migrations", db.SuperDSN)
	require.NoError(t, err)
	require.NoError(t, m.Steps(1))
	_, _ = m.Close()
	require.NoError(t, database.RunServiceMigrations(db.SuperDSN, "../../migrations", "audit"))
	_, err = db.Super.Exec(ctx, `GRANT SELECT, INSERT ON ALL TABLES IN SCHEMA public TO dms_app`)
	require.NoError(t, err)

	tenant := uuid.New()
	_, err = db.Super.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, 'o', $2)`, tenant, tenant.String())
	require.NoError(t, err)
	_, err = db.Super.Exec(ctx, `INSERT INTO audit_events (id, tenant_id, event_hash, previous_hash, actor, actor_type, action)
		VALUES ($1, $2, 'v2:not-canonical', '', '', 'system', 'x')`, uuid.New(), tenant)
	require.NoError(t, err)
	t.Setenv("SEDOC_AUDIT_SIGNING_KEY", "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA=") // 32-byte seed 1..32

	var out, errOut bytes.Buffer
	code := run(ctx, []string{"--dsn", db.AppDSN, "--apply", "--operator", "ops@example.com"}, &out, &errOut)
	require.NotZero(t, code, "must refuse to run as dms_app")
	require.Contains(t, errOut.String(), "BYPASSRLS")

	var hash string
	var markers int
	require.NoError(t, db.Super.QueryRow(ctx,
		`SELECT event_hash, (SELECT count(*) FROM audit_events WHERE action = 'audit.chain_rebaselined')
		 FROM audit_events WHERE tenant_id = $1 AND action = 'x'`, tenant).Scan(&hash, &markers))
	require.Equal(t, "v2:not-canonical", hash, "nothing may be written")
	require.Zero(t, markers)

	// The owner role runs: a dry run by default (exit 0, still no writes),
	// --apply rewrites.
	out.Reset()
	errOut.Reset()
	require.Zero(t, run(ctx, []string{"--dsn", db.SuperDSN}, &out, &errOut), errOut.String())
	require.Contains(t, out.String(), "dry-run")
	require.NoError(t, db.Super.QueryRow(ctx,
		`SELECT event_hash FROM audit_events WHERE tenant_id = $1 AND action = 'x'`, tenant).Scan(&hash))
	require.Equal(t, "v2:not-canonical", hash)

	require.Zero(t, run(ctx, []string{"--dsn", db.SuperDSN, "--apply", "--operator", "ops@example.com",
		"--tenant", tenant.String()}, &out, &errOut), errOut.String())
	require.NoError(t, db.Super.QueryRow(ctx,
		`SELECT event_hash FROM audit_events WHERE tenant_id = $1 AND action = 'x'`, tenant).Scan(&hash))
	require.NotEqual(t, "v2:not-canonical", hash)
}

// H-12 round 4: a re-baselined chain without a signed checkpoint is an
// uncertified tail. --apply with no (or an invalid) signing key is refused
// before any tenant is touched; a dry run still works and says apply will
// need the key.
func TestRebaseline_ApplyRequiresSigningKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	db := testutil.NewProdPostureDB(ctx, t, "")
	m, err := migrate.New("file://../../../../services/document/migrations", db.SuperDSN)
	require.NoError(t, err)
	require.NoError(t, m.Steps(1))
	_, _ = m.Close()
	require.NoError(t, database.RunServiceMigrations(db.SuperDSN, "../../migrations", "audit"))

	tenant := uuid.New()
	_, err = db.Super.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, 'o', $2)`, tenant, tenant.String())
	require.NoError(t, err)
	_, err = db.Super.Exec(ctx, `INSERT INTO audit_events (id, tenant_id, event_hash, previous_hash, actor, actor_type, action)
		VALUES ($1, $2, 'v2:not-canonical', '', '', 'system', 'x')`, uuid.New(), tenant)
	require.NoError(t, err)

	unchanged := func() {
		t.Helper()
		var hash string
		var markers int
		require.NoError(t, db.Super.QueryRow(ctx,
			`SELECT event_hash, (SELECT count(*) FROM audit_events WHERE action = 'audit.chain_rebaselined')
			 FROM audit_events WHERE tenant_id = $1 AND action = 'x'`, tenant).Scan(&hash, &markers))
		require.Equal(t, "v2:not-canonical", hash, "no row may change")
		require.Zero(t, markers, "no marker may be written")
	}

	for _, key := range []string{"", "not-a-valid-seed"} {
		t.Setenv("SEDOC_AUDIT_SIGNING_KEY", key)
		var out, errOut bytes.Buffer
		code := run(ctx, []string{"--dsn", db.SuperDSN, "--apply", "--operator", "ops@example.com"}, &out, &errOut)
		require.Equal(t, 4, code, "key %q: %s", key, errOut.String())
		require.Contains(t, errOut.String(), "SEDOC_AUDIT_SIGNING_KEY")
		unchanged()
	}

	t.Setenv("SEDOC_AUDIT_SIGNING_KEY", "")
	var out, errOut bytes.Buffer
	require.Zero(t, run(ctx, []string{"--dsn", db.SuperDSN}, &out, &errOut), errOut.String())
	require.Contains(t, errOut.String(), "--apply will require SEDOC_AUDIT_SIGNING_KEY")
	unchanged()
}
