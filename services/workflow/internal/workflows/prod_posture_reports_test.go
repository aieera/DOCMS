//go:build integration && prodposture
// +build integration,prodposture

// L-11 (workflow part): the scheduled-report reconciler full-table-scanned
// saved_reports on the raw pool. saved_reports is FORCE RLS, so under the
// prod NOBYPASSRLS role the scan returns zero rows: no report schedule is
// ever created, and the orphan sweep then DELETES every existing report
// schedule because no row "survives". This drives the real reconcile
// against a dms_app database with a recording Temporal schedule fake.
package workflows_test

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/workflow/internal/workflows"
)

// fakeSchedules is an in-memory Temporal schedule store. Unimplemented
// methods panic through the nil embedded interfaces.
type fakeSchedules struct {
	client.ScheduleClient
	live    map[string]bool
	tenant  map[string]string // schedule id → tenant in its workflow args
	created []string
	deleted []string
}

func (f *fakeSchedules) Create(_ context.Context, o client.ScheduleOptions) (client.ScheduleHandle, error) {
	if f.live[o.ID] {
		return nil, serviceerror.NewAlreadyExist("schedule already exists")
	}
	f.live[o.ID] = true
	f.created = append(f.created, o.ID)
	if a, ok := o.Action.(*client.ScheduleWorkflowAction); ok && len(a.Args) == 1 {
		if in, ok := a.Args[0].(workflows.ScheduledReportInput); ok {
			f.tenant[o.ID] = in.TenantID
		}
	}
	return &fakeHandle{f: f, id: o.ID}, nil
}

func (f *fakeSchedules) GetHandle(_ context.Context, id string) client.ScheduleHandle {
	return &fakeHandle{f: f, id: id}
}

func (f *fakeSchedules) List(context.Context, client.ScheduleListOptions) (client.ScheduleListIterator, error) {
	ids := make([]string, 0, len(f.live))
	for id := range f.live {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return &fakeIter{ids: ids}, nil
}

type fakeHandle struct {
	client.ScheduleHandle
	f  *fakeSchedules
	id string
}

func (h *fakeHandle) GetID() string { return h.id }

func (h *fakeHandle) Describe(context.Context) (*client.ScheduleDescription, error) {
	if !h.f.live[h.id] {
		return nil, serviceerror.NewNotFound("schedule not found")
	}
	return &client.ScheduleDescription{}, nil
}

func (h *fakeHandle) Delete(context.Context) error {
	if !h.f.live[h.id] {
		return serviceerror.NewNotFound("schedule not found")
	}
	delete(h.f.live, h.id)
	h.f.deleted = append(h.f.deleted, h.id)
	return nil
}

type fakeIter struct {
	ids []string
	i   int
}

func (it *fakeIter) HasNext() bool { return it.i < len(it.ids) }

func (it *fakeIter) Next() (*client.ScheduleListEntry, error) {
	if !it.HasNext() {
		return nil, errors.New("iterator exhausted")
	}
	it.i++
	return &client.ScheduleListEntry{ID: it.ids[it.i-1]}, nil
}

type fakeTemporal struct {
	client.Client
	sc *fakeSchedules
}

func (t *fakeTemporal) ScheduleClient() client.ScheduleClient { return t.sc }

func TestProdPosture_ScheduledReportsReconcile(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)

	db := testutil.NewProdPostureDB(ctx, t, "")
	// organizations is the non-RLS tenant registry the sweep enumerates;
	// saved_reports is document 000093 verbatim minus the organizations FK.
	_, err := db.Super.Exec(ctx, `
		CREATE TABLE organizations (
			id         UUID PRIMARY KEY,
			name       TEXT NOT NULL,
			deleted_at TIMESTAMPTZ
		);
		CREATE TABLE saved_reports (
			tenant_id        UUID NOT NULL,
			id               UUID NOT NULL DEFAULT gen_random_uuid(),
			name             TEXT NOT NULL,
			description      TEXT NOT NULL DEFAULT '',
			query            JSONB NOT NULL,
			chart_type       TEXT NOT NULL DEFAULT 'table',
			schedule_enabled BOOLEAN NOT NULL DEFAULT false,
			schedule_cron    TEXT NOT NULL DEFAULT '',
			schedule_interval_minutes INT NOT NULL DEFAULT 0,
			channels         TEXT[] NOT NULL DEFAULT '{in_app}',
			created_by       UUID NOT NULL,
			created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
			last_run_at      TIMESTAMPTZ,
			PRIMARY KEY (tenant_id, id)
		);
		ALTER TABLE saved_reports ENABLE ROW LEVEL SECURITY;
		ALTER TABLE saved_reports FORCE ROW LEVEL SECURITY;
		CREATE POLICY saved_reports_isolation ON saved_reports
			USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
			WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dms_app;
	`)
	require.NoError(t, err)

	tenantA, tenantB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	_, err = db.Super.Exec(ctx, `INSERT INTO organizations (id, name) VALUES ($1, 'A'), ($2, 'B')`, tenantA, tenantB)
	require.NoError(t, err)

	seed := func(tenant uuid.UUID, enabled bool) string {
		id := uuid.Must(uuid.NewV7())
		require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
				INSERT INTO saved_reports (tenant_id, id, name, query, schedule_enabled, schedule_interval_minutes, created_by)
				VALUES ($1, $2, $3, '{}'::jsonb, $4, 30, $5)`,
				tenant, id, "r-"+id.String(), enabled, uuid.Must(uuid.NewV7()))
			return err
		}))
		return id.String()
	}
	newA := seed(tenantA, true)       // enabled, no schedule yet → create
	keptB := seed(tenantB, true)      // enabled, schedule exists → keep
	disabledB := seed(tenantB, false) // disabled, schedule exists → delete
	orphan := uuid.NewString()        // schedule whose row is gone → delete

	id := workflows.ScheduledReportScheduleID
	fs := &fakeSchedules{
		live: map[string]bool{
			id(keptB):     true,
			id(disabledB): true,
			id(orphan):    true,
			// Not a report schedule: the sweep must leave it alone.
			"saved-search-alert-x": true,
		},
		tenant: map[string]string{},
	}

	created, deleted, err := workflows.ReconcileScheduledReportSchedules(ctx, db.App, &fakeTemporal{sc: fs}, "q")
	require.NoError(t, err)

	require.Equal(t, []string{id(newA)}, fs.created, "the enabled report's schedule must be created under prod posture")
	require.Equal(t, tenantA.String(), fs.tenant[id(newA)], "the schedule runs the report in its own tenant")
	require.ElementsMatch(t, []string{id(disabledB), id(orphan)}, fs.deleted)
	require.True(t, fs.live[id(keptB)], "an enabled report's schedule must survive the orphan sweep")
	require.True(t, fs.live["saved-search-alert-x"])
	require.Equal(t, 1, created)
	require.Equal(t, 2, deleted)
}
