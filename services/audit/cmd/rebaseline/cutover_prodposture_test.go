//go:build integration && prodposture

package main

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

const testSigningKey = "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA=" // seed bytes 1..32

type seededEvent struct {
	id string
	at time.Time
}

// newCutoverDB migrates the audit schema and seeds one tenant with
// non-canonical events at the given times (the shape a pre-fix chain, or
// a tampered one, has to this tool: stored hashes that do not recompute).
func newCutoverDB(ctx context.Context, t *testing.T, times ...time.Time) (*testutil.ProdPostureDB, uuid.UUID, []seededEvent) {
	t.Helper()
	db := testutil.NewProdPostureDB(ctx, t, "")
	m, err := migrate.New("file://../../../../services/document/migrations", db.SuperDSN)
	require.NoError(t, err)
	require.NoError(t, m.Steps(1))
	_, _ = m.Close()
	require.NoError(t, database.RunServiceMigrations(db.SuperDSN, "../../migrations", "audit"))
	tenant := uuid.New()
	_, err = db.Super.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, 'o', $2)`, tenant, tenant.String())
	require.NoError(t, err)
	var out []seededEvent
	for i, at := range times {
		id := uuid.Must(uuid.NewV7()).String()
		_, err = db.Super.Exec(ctx, `INSERT INTO audit_events (id, tenant_id, event_hash, previous_hash, actor, actor_type, action, created_at)
			VALUES ($1, $2, $3, '', '', 'system', 'x', $4)`, id, tenant, fmt.Sprintf("v2:not-canonical-%d", i), at)
		require.NoError(t, err)
		out = append(out, seededEvent{id: id, at: at.UTC()})
	}
	return db, tenant, out
}

func countMarkers(ctx context.Context, t *testing.T, pool *pgxpool.Pool) (int, string) {
	t.Helper()
	var n int
	var details string
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*), COALESCE(max(details::text), '')
		FROM audit_events WHERE action = 'audit.chain_rebaselined'`).Scan(&n, &details))
	return n, details
}

// H-12 round 5: the tool could not tell a pre-fix chain from a tampered
// one. --apply now requires --cutover (when the canonical-hash fix was
// deployed) and refuses a tenant — exit 5, nothing written — when any
// differing event was written at or after it: that is tampering (or a
// bug), not a pre-fix chain. The marker records the cutover.
func TestRebaseline_RefusesDifferingEventAfterCutover(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	t.Setenv("SEDOC_AUDIT_SIGNING_KEY", testSigningKey)
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	db, _, _ := newCutoverDB(ctx, t, base, base.Add(time.Hour), base.Add(48*time.Hour))
	cutover := base.Add(24 * time.Hour).Format(time.RFC3339)

	var out, errOut bytes.Buffer
	require.Equal(t, 2, run(ctx, []string{"--dsn", db.SuperDSN, "--apply", "--operator", "ops"}, &out, &errOut),
		"--apply without --cutover is a usage error")

	out.Reset()
	errOut.Reset()
	code := run(ctx, []string{"--dsn", db.SuperDSN, "--apply", "--operator", "ops", "--cutover", cutover}, &out, &errOut)
	require.Equal(t, 5, code, errOut.String())
	require.Contains(t, errOut.String(), "investigate")
	var hashes int
	require.NoError(t, db.Super.QueryRow(ctx,
		`SELECT count(*) FROM audit_events WHERE event_hash LIKE 'v2:not-canonical-%'`).Scan(&hashes))
	require.Equal(t, 3, hashes, "a refused tenant must be left untouched")
	n, _ := countMarkers(ctx, t, db.Super)
	require.Zero(t, n)

	// With the cutover after every differing event, the same chain is a
	// pre-fix chain and is re-baselined; the marker records the cutover.
	late := base.Add(72 * time.Hour).Format(time.RFC3339)
	require.Zero(t, run(ctx, []string{"--dsn", db.SuperDSN, "--apply", "--operator", "ops", "--cutover", late}, &out, &errOut),
		errOut.String())
	n, details := countMarkers(ctx, t, db.Super)
	require.Equal(t, 1, n)
	require.Contains(t, details, late)
}

// H-12 round 5: the dry run shows what --apply would sign: per tenant the
// differing count, the first and last differing event (id, created_at)
// and, given --cutover, how many differ at or after it.
func TestRebaseline_DryRunListsDifferingRange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	db, tenant, ev := newCutoverDB(ctx, t, base, base.Add(time.Hour), base.Add(48*time.Hour))

	var out, errOut bytes.Buffer
	require.Zero(t, run(ctx, []string{"--dsn", db.SuperDSN, "--cutover", base.Add(24 * time.Hour).Format(time.RFC3339)}, &out, &errOut),
		errOut.String())
	line := out.String()
	require.Contains(t, line, "tenant="+tenant.String())
	require.Contains(t, line, "differing=3")
	require.Contains(t, line, "first="+ev[0].id+"@"+ev[0].at.Format(time.RFC3339Nano))
	require.Contains(t, line, "last="+ev[2].id+"@"+ev[2].at.Format(time.RFC3339Nano))
	require.Contains(t, line, "at_or_after_cutover=1")
	n, _ := countMarkers(ctx, t, db.Super)
	require.Zero(t, n, "a dry run writes nothing")
}
