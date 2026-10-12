//go:build integration && prodposture

package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/services/audit/internal/model"
	"github.com/aieera/sedoc/services/audit/internal/repository"
)

// L-10: audit_events partitions carried no RLS and received direct grants
// (UPDATE on the four document-000001 partitions, SELECT/INSERT/UPDATE/
// DELETE on every runtime-created one through the init-db default
// privileges). Querying a partition by name bypasses the parent's tenant
// policies and its WORM revoke. Every partition — existing, DEFAULT and
// created later by ensure_audit_partition_range — must be ENABLE + FORCE
// RLS with no direct grant to dms_app or dms_readonly, while access
// through the parent keeps working.
func TestProdPosture_PartitionDirectAccessDenied(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	db := newProdPostureAuditDB(ctx, t)
	repo := repository.New(db.App)
	svc := New(Config{Repo: repo, Logger: zerolog.Nop()})
	tenant := uuid.Must(uuid.NewV7())

	// A partition created after the migration, the way the service's
	// maintainer creates them.
	_, err := db.Super.Exec(ctx, `SELECT ensure_audit_partition_range(DATE '2031-01-01', DATE '2031-01-01')`)
	require.NoError(t, err)

	// Through the parent everything still works for dms_app.
	require.NoError(t, svc.RecordAccessDenied(ctx, tenant.String(), "", "", "read", "/x", "member"))
	got, _, err := repo.List(ctx, model.ListFilter{TenantID: tenant.String()})
	require.NoError(t, err)
	require.Len(t, got, 1)

	type part struct {
		name          string
		rls, force    bool
		app, readonly bool
	}
	rows, err := db.Super.Query(ctx, `
		SELECT c.relname, c.relrowsecurity, c.relforcerowsecurity,
		       has_table_privilege('dms_app', c.oid, 'SELECT,INSERT,UPDATE,DELETE'),
		       has_table_privilege('dms_readonly', c.oid, 'SELECT')
		FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		WHERE i.inhparent = 'public.audit_events'::regclass ORDER BY c.relname`)
	require.NoError(t, err)
	var parts []part
	for rows.Next() {
		var p part
		require.NoError(t, rows.Scan(&p.name, &p.rls, &p.force, &p.app, &p.readonly))
		parts = append(parts, p)
	}
	rows.Close()
	names := map[string]bool{}
	for _, p := range parts {
		names[p.name] = true
		require.True(t, p.rls && p.force, "%s must be ENABLE + FORCE ROW LEVEL SECURITY", p.name)
		require.False(t, p.app, "%s must carry no direct grant to dms_app", p.name)
		require.False(t, p.readonly, "%s must carry no direct grant to dms_readonly", p.name)
	}
	for _, want := range []string{"audit_events_2026_04", "audit_events_2026_07", "audit_events_default", "audit_events_2031_01"} {
		require.True(t, names[want], "expected partition %s", want)
	}

	// Direct access by name is refused even inside the right tenant context.
	for _, q := range []string{
		`SELECT count(*) FROM audit_events_2026_07`,
		`UPDATE audit_events_2026_04 SET action = 'x'`,
		`SELECT count(*) FROM audit_events_default`,
		`DELETE FROM audit_events_2031_01`,
	} {
		err := database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx, q)
			return e
		})
		require.ErrorContains(t, err, "permission denied", q)
	}

	// Checkpoints are append-only for the app role too.
	err = database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `DELETE FROM audit_checkpoints`)
		return e
	})
	require.ErrorContains(t, err, "permission denied")
}

// The FORCE flag on partitions must not break partition maintenance when
// the table owner is an ordinary role (no superuser, no BYPASSRLS): the
// DEFAULT→month relocation and the DEFAULT-rows gauge read partitions
// directly as the owner.
func TestProdPosture_PartitionMaintenanceUnderNonBypassOwner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	db := newProdPostureAuditDB(ctx, t)
	_, err := db.Super.Exec(ctx, `
		CREATE ROLE audit_owner NOLOGIN NOBYPASSRLS;
		GRANT CREATE, USAGE ON SCHEMA public TO audit_owner;
		DO $$ DECLARE r record; BEGIN
		  EXECUTE 'ALTER TABLE public.audit_events OWNER TO audit_owner';
		  FOR r IN SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		           WHERE i.inhparent = 'public.audit_events'::regclass LOOP
		    EXECUTE format('ALTER TABLE public.%I OWNER TO audit_owner', r.relname);
		  END LOOP;
		END $$;
		ALTER FUNCTION ensure_audit_partition_range(date, date) OWNER TO audit_owner;
		ALTER FUNCTION audit_default_partition_rows() OWNER TO audit_owner;
		ALTER FUNCTION audit_secure_partition(regclass) OWNER TO audit_owner;
	`)
	require.NoError(t, err)

	repo := repository.New(db.App)
	tenant := uuid.Must(uuid.NewV7())
	ev := &model.AuditEvent{
		ID: uuid.Must(uuid.NewV7()).String(), TenantID: tenant.String(), Action: "far.future",
		EventHash: "h", Details: []byte(`{}`), CreatedAt: time.Date(2035, 3, 5, 0, 0, 0, 0, time.UTC),
	}
	require.NoError(t, repo.AppendChained(ctx, ev, keepHash), "an out-of-window row lands in DEFAULT")
	n, err := repo.DefaultPartitionRows(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "the gauge must see DEFAULT rows as a non-bypass owner")

	_, err = db.Super.Exec(ctx, `SELECT ensure_audit_partition_range(DATE '2035-03-01', DATE '2035-03-01')`)
	require.NoError(t, err, "relocating DEFAULT rows must work as a non-bypass owner")
	n, err = repo.DefaultPartitionRows(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
	var landed string
	require.NoError(t, db.Super.QueryRow(ctx,
		`SELECT tableoid::regclass::text FROM audit_events WHERE id = $1`, ev.ID).Scan(&landed))
	require.Equal(t, "audit_events_2035_03", landed)
}

// keepHash persists the fixture's own event_hash (these tests are about
// partition routing, not hash content).
func keepHash(_ string, e *model.AuditEvent) string { return e.EventHash }
