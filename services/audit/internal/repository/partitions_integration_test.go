//go:build integration

// BUG-01 — audit_events partition maintenance, end to end against a real
// Postgres.
//
// The bug: audit_events is PARTITION BY RANGE (created_at) and document
// migration 000001 created exactly four monthly partitions, the last
// ending 2026-08-01, with a comment claiming operators would roll them
// forward via a cron job that does not exist in this repo. From
// 2026-08-01 every INSERT failed with
//
//	ERROR: no partition of relation "audit_events" found for row (23514)
//
// so the audit log was empty and legal hold, e-discovery, SIEM
// forwarding, GDPR/DSR and retention all had nothing to work with.
//
// These assertions only hold with audit migration 000005 applied, and
// none of them can be made without a real database — tuple routing,
// DEFAULT-partition capture and the DEFAULT→month relocation are all
// Postgres behaviours, not Go logic.
//
// Run with:
//
//	go test -tags integration ./services/audit/internal/repository/...

package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // migrate driver
	_ "github.com/golang-migrate/migrate/v4/source/file"       // migrate source
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/audit/internal/model"
	"github.com/aieera/sedoc/services/audit/internal/repository"
)

// newAuditSchemaPool brings up an ephemeral Postgres carrying exactly the
// schema audit_events needs: document migration 000001 (the partitioned
// table itself is that service's schema-of-record) plus the audit
// service's own migrations under audit_schema_migrations, the same pair
// `make migrate-up SERVICE=audit` produces. The full document migrations
// directory cannot run standalone — 000021 depends on tables the
// intelligence service creates — so only its first step is applied, the
// same approach as prod_posture_repro_test.go.
func newAuditSchemaPool(ctx context.Context, t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn, cleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err, "start postgres container")
	t.Cleanup(cleanup)

	m, err := migrate.New("file://../../../../services/document/migrations", dsn)
	require.NoError(t, err, "init document migrator")
	require.NoError(t, m.Steps(1), "document migration 000001 (audit_events)")
	_, _ = m.Close()

	require.NoError(t,
		database.RunServiceMigrations(dsn, "../../migrations", "audit"),
		"audit service migrations")

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err, "open pool")
	t.Cleanup(pool.Close)
	return pool
}

func newAuditEvent(tenantID uuid.UUID, action string, at time.Time) *model.AuditEvent {
	return &model.AuditEvent{
		ID:          uuid.Must(uuid.NewV7()).String(),
		TenantID:    tenantID.String(),
		Action:      action,
		EventHash:   "hash-" + action,
		Details:     []byte(`{}`),
		SourceEvent: "dms.test.partitions.v1",
		CreatedAt:   at.Truncate(time.Microsecond),
	}
}

// TestPartitions_InsertAtWallClockTimeSucceeds is the direct BUG-01
// regression: writing an audit event stamped "now" through the real
// repository. Before migration 000005 this failed with SQLSTATE 23514 on
// any date past 2026-08-01 and the row was lost.
func TestPartitions_InsertAtWallClockTimeSucceeds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	pool := newAuditSchemaPool(ctx, t)
	repo := repository.New(pool)
	tenantID := uuid.Must(uuid.NewV7())

	ev := newAuditEvent(tenantID, "document.viewed", time.Now().UTC())
	require.NoError(t, repo.Insert(ctx, ev),
		"an audit event stamped with the current wall clock MUST be storable; "+
			"SQLSTATE 23514 here means no partition covers today (BUG-01)")

	got, _, err := repo.List(ctx, model.ListFilter{TenantID: tenantID.String()})
	require.NoError(t, err)
	require.Len(t, got, 1, "the inserted event must be readable back")

	// It must land in a real monthly partition, not the safety net.
	var landedIn string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT tableoid::regclass::text FROM audit_events WHERE id = $1`, ev.ID).Scan(&landedIn))
	require.NotEqual(t, "audit_events_default", landedIn,
		"a normally-timed event should route to its month partition, not DEFAULT")
}

// TestPartitions_DefaultCapturesOutOfWindowRows is the guarantee that
// makes the bug unrepeatable: whatever created_at arrives — clock skew, a
// backfill, a maintainer that has been dead for years — the row is
// captured rather than rejected. For an audit table a row in a
// suboptimal partition beats a dropped row every time.
func TestPartitions_DefaultCapturesOutOfWindowRows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	pool := newAuditSchemaPool(ctx, t)
	repo := repository.New(pool)
	tenantID := uuid.Must(uuid.NewV7())

	// Far outside any window the maintainer provisions.
	farFuture := time.Date(2099, time.March, 15, 12, 0, 0, 0, time.UTC)
	ev := newAuditEvent(tenantID, "document.downloaded", farFuture)
	require.NoError(t, repo.Insert(ctx, ev),
		"an out-of-window created_at must be CAPTURED by the DEFAULT partition, never rejected")

	var landedIn string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT tableoid::regclass::text FROM audit_events WHERE id = $1`, ev.ID).Scan(&landedIn))
	require.Equal(t, "audit_events_default", landedIn)

	n, err := repo.DefaultPartitionRows(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "DefaultPartitionRows must surface stranded rows so the gauge can alert on them")

	// And it is a first-class row: readable through the ordinary
	// tenant-scoped read path, not quarantined.
	got, _, err := repo.List(ctx, model.ListFilter{TenantID: tenantID.String()})
	require.NoError(t, err)
	require.Len(t, got, 1, "a row in DEFAULT must still be queryable through the parent table")
}

// TestPartitions_EnsureIsIdempotent pins the property that lets the
// maintainer run on every boot and every tick: a second pass over an
// already-maintained database creates nothing and changes nothing.
func TestPartitions_EnsureIsIdempotent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	pool := newAuditSchemaPool(ctx, t)
	repo := repository.New(pool)

	countPartitions := func() int {
		var n int
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
			WHERE i.inhparent = 'public.audit_events'::regclass`).Scan(&n))
		return n
	}

	// The migration already ran ensure; a fresh pass must be a no-op.
	before := countPartitions()
	created, err := repo.EnsurePartitions(ctx, 12)
	require.NoError(t, err)
	require.Equal(t, before, countPartitions(), "an idempotent pass must not change the partition set")

	created2, err := repo.EnsurePartitions(ctx, 12)
	require.NoError(t, err)
	require.Zero(t, created2, "a repeat pass must create zero partitions; got %d (first pass created %d)", created2, created)

	// Widening the window does create partitions, and re-running at the
	// wider window then creates none.
	widened, err := repo.EnsurePartitions(ctx, 36)
	require.NoError(t, err)
	require.Positive(t, widened, "widening months_ahead must provision the extra months")
	again, err := repo.EnsurePartitions(ctx, 36)
	require.NoError(t, err)
	require.Zero(t, again, "the widened window must also be idempotent")
}

// TestPartitions_DefaultRowsAreRelocatedWhenTheirMonthIsCreated covers the
// self-healing half: rows that fell into DEFAULT are moved into the right
// monthly partition once it exists, without loss and without blocking the
// partition's creation (Postgres refuses to attach a range the DEFAULT
// partition still holds rows for, so a naive maintainer would wedge here
// permanently).
func TestPartitions_DefaultRowsAreRelocatedWhenTheirMonthIsCreated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	pool := newAuditSchemaPool(ctx, t)
	repo := repository.New(pool)
	tenantID := uuid.Must(uuid.NewV7())

	stranded := time.Date(2099, time.March, 15, 12, 0, 0, 0, time.UTC)
	ev := newAuditEvent(tenantID, "record.disposed", stranded)
	require.NoError(t, repo.Insert(ctx, ev))

	n, err := repo.DefaultPartitionRows(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "precondition: the row is in DEFAULT")

	// Provision exactly that month. This must succeed *and* drain DEFAULT.
	var createdCount int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT ensure_audit_partition_range($1::date, $1::date)`, "2099-03-01").Scan(&createdCount),
		"creating a partition whose range DEFAULT still holds rows for must succeed by relocating them first")
	require.Equal(t, 1, createdCount)

	var landedIn string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT tableoid::regclass::text FROM audit_events WHERE id = $1`, ev.ID).Scan(&landedIn))
	require.Equal(t, "audit_events_2099_03", landedIn, "the stranded row must have been relocated into its month")

	n, err = repo.DefaultPartitionRows(ctx)
	require.NoError(t, err)
	require.Zero(t, n, "DEFAULT must be drained of rows that now have a home")

	// Nothing was lost or corrupted by the move.
	got, _, err := repo.List(ctx, model.ListFilter{TenantID: tenantID.String()})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, ev.EventHash, got[0].EventHash, "relocation must preserve the hash-chain payload byte for byte")
	require.True(t, got[0].CreatedAt.Equal(ev.CreatedAt), "relocation must preserve created_at")
}
