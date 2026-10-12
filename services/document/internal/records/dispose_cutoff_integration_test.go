//go:build integration

// D-7: a record's retention cutoff was computed only for declaration-
// triggered schedules, and Dispose skipped the retention guard whenever the
// cutoff was NULL — so every record filed under a creation / fixed_date /
// event / superseded schedule could be destroyed the moment it was declared.
// Declare now computes the cutoff for creation (document created_at + period)
// and fixed_date (end of the declaration's calendar year + period), and
// Dispose refuses a scheduled record whose cutoff is still unresolved.
package records_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/document/internal/records"
)

type recFixture struct {
	ctx    context.Context
	pool   *pgxpool.Pool
	svc    *records.Service
	tenant uuid.UUID
	user   uuid.UUID
	ws     uuid.UUID
	folder uuid.UUID
}

func newRecFixture(t *testing.T) *recFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	dsn, cleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	require.NoError(t, database.RunMigrations(dsn, "../../migrations"))
	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	f := &recFixture{ctx: ctx, pool: pool, svc: records.New(pool),
		tenant: uuid.Must(uuid.NewV7()), user: uuid.Must(uuid.NewV7()),
		ws: uuid.Must(uuid.NewV7()), folder: uuid.Must(uuid.NewV7())}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, 'T', $2)`, []any{f.tenant, "t-" + f.tenant.String()[:8]}},
		{`INSERT INTO users (tenant_id, id, email, display_name, role) VALUES ($1, $2, $3, 'U', 'admin')`,
			[]any{f.tenant, f.user, "u-" + f.user.String()[:8] + "@test.local"}},
		{`INSERT INTO workspaces (tenant_id, id, name) VALUES ($1, $2, 'ws')`, []any{f.tenant, f.ws}},
		{`INSERT INTO folders (tenant_id, id, workspace_id, name, path, depth) VALUES ($1, $2, $3, 'root', 'root', 0)`,
			[]any{f.tenant, f.folder, f.ws}},
	} {
		_, err := pool.Exec(ctx, q.sql, q.args...)
		require.NoError(t, err)
	}
	return f
}

// doc inserts an active document created `ageDays` ago.
func (f *recFixture) doc(t *testing.T, ageDays int) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	_, err := f.pool.Exec(f.ctx, `
		INSERT INTO documents (tenant_id, id, workspace_id, folder_id, title, lifecycle_state,
		                       created_by, updated_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'active', $6, $6, now() - make_interval(days => $7), now())`,
		f.tenant, id, f.ws, f.folder, "doc-"+id.String()[:8], f.user, ageDays)
	require.NoError(t, err)
	return id
}

// declareUnder creates a schedule + category and declares docID against it.
func (f *recFixture) declareUnder(t *testing.T, docID uuid.UUID, trigger string, days int) *records.Record {
	t.Helper()
	sched, err := f.svc.CreateSchedule(f.ctx, f.tenant, records.ScheduleInput{
		Name: trigger + "-" + uuid.NewString()[:8], TriggerEvent: trigger,
		RetentionPeriodDays: days, DispositionAction: "destroy",
	})
	require.NoError(t, err)
	cat, err := f.svc.CreateCategory(f.ctx, f.tenant, records.CategoryInput{
		Name: "cat-" + uuid.NewString()[:8], RetentionScheduleID: &sched.ID,
	})
	require.NoError(t, err)
	rec, err := f.svc.Declare(f.ctx, f.tenant, f.user, records.DeclareInput{DocumentID: docID, CategoryID: cat.ID})
	require.NoError(t, err)
	return rec
}

func (f *recFixture) dispose(recID uuid.UUID) error {
	_, err := f.svc.Dispose(f.ctx, f.tenant, f.user, recID, records.DisposeInput{Certify: true, Reason: "schedule"})
	return err
}

func TestDispose_UnresolvedCutoffRefused(t *testing.T) {
	f := newRecFixture(t)

	// event / superseded: nothing resolves the cutoff yet → never disposable.
	for _, trig := range []string{"event", "superseded"} {
		rec := f.declareUnder(t, f.doc(t, 3650), trig, 0)
		require.Nil(t, rec.CutoffDate)
		err := f.dispose(rec.ID)
		require.Error(t, err, "%s: an unresolved cutoff must not be treated as 'no retention'", trig)
		require.Equal(t, vdmserr.KindConflict, vdmserr.KindOf(err))
		require.ErrorContains(t, err, "cutoff")
	}

	// creation: clock starts at the document's creation.
	young := f.declareUnder(t, f.doc(t, 10), "creation", 365)
	require.NotNil(t, young.CutoffDate)
	require.ErrorContains(t, f.dispose(young.ID), "within its retention period")
	old := f.declareUnder(t, f.doc(t, 400), "creation", 365)
	require.NotNil(t, old.CutoffDate)
	require.True(t, old.CutoffDate.Before(time.Now()))
	require.NoError(t, f.dispose(old.ID))

	// fixed_date: clock starts at the end of the declaration's calendar year.
	fixed := f.declareUnder(t, f.doc(t, 3650), "fixed_date", 0)
	require.NotNil(t, fixed.CutoffDate)
	now := time.Now().UTC()
	require.Equal(t, time.Date(now.Year()+1, 1, 1, 0, 0, 0, 0, time.UTC), fixed.CutoffDate.UTC())
	require.ErrorContains(t, f.dispose(fixed.ID), "within its retention period")

	// A record declared before this fix (creation trigger, cutoff left NULL)
	// is resolved at disposition time rather than waved through.
	legacy := f.declareUnder(t, f.doc(t, 30), "creation", 365)
	_, err := f.pool.Exec(f.ctx, `UPDATE records SET cutoff_date = NULL WHERE tenant_id = $1 AND id = $2`, f.tenant, legacy.ID)
	require.NoError(t, err)
	require.ErrorContains(t, f.dispose(legacy.ID), "within its retention period")

	// declaration trigger and unscheduled records are unchanged.
	decl := f.declareUnder(t, f.doc(t, 1), "declaration", 0)
	require.NoError(t, f.dispose(decl.ID))
}
