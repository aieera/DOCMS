//go:build integration
// +build integration

// CompleteTask correctness against a REAL Postgres (not sqlmock), because
// the two defects this pins are invisible to a mock:
//
//  1. Parse error: the old query was `UPDATE … WHERE … ORDER BY … LIMIT`,
//     which Postgres rejects outright — so EVERY task completion failed and
//     workflow tasks never left 'pending' (approvals/review/DSR/retention
//     all stalled at the first human step). sqlmock would happily "match"
//     the invalid SQL; only a real server proves it parses.
//  2. CHECK violation: callers pass raw workflow outcomes ('approved',
//     'cancelled', 'sign', 'decline', …). workflow_tasks.status has a CHECK
//     constraint that only permits pending/in_progress/completed/rejected/
//     delegated/escalated/skipped, so writing the raw outcome as status
//     would be rejected. Only a real server enforces the CHECK.
package activities

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

// workflowTasksDDL is workflow_tasks verbatim from document migration
// 000001 (+ 000005's document_id / nullable step_id), created standalone
// so the test skips the full document chain (broken at 000021 on a clean
// DB). The status CHECK is the exact constraint under test. FKs are
// dropped: they'd require the organizations/users/instances/documents
// parents and add nothing to what these cases prove.
const workflowTasksDDL = `
CREATE TABLE workflow_tasks (
	tenant_id      UUID NOT NULL,
	id             UUID NOT NULL DEFAULT gen_random_uuid(),
	instance_id    UUID NOT NULL,
	step_id        TEXT,
	step_name      TEXT NOT NULL,
	assignee_id    UUID,
	status         TEXT NOT NULL DEFAULT 'pending'
	                    CHECK (status IN ('pending', 'in_progress', 'completed',
	                                      'rejected', 'delegated', 'escalated', 'skipped')),
	due_at         TIMESTAMPTZ,
	completed_at   TIMESTAMPTZ,
	completed_by   UUID,
	outcome        TEXT,
	notes          TEXT,
	delegated_to   UUID,
	document_id    UUID,
	created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (tenant_id, id)
);
`

func newTaskActivities(t *testing.T) (*Activities, context.Context, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)

	dsn, cleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(cleanup)

	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, workflowTasksDDL)
	require.NoError(t, err)

	return &Activities{Pool: pool, Log: zerolog.Nop()}, ctx, uuid.Must(uuid.NewV7()).String()
}

func seedPending(t *testing.T, a *Activities, ctx context.Context, tenant, instance string) {
	t.Helper()
	require.NoError(t, a.CreateTask(ctx, tenant, instance,
		uuid.Must(uuid.NewV7()).String(), "review", uuid.Must(uuid.NewV7()).String()))
}

// TestCompleteTask_ValidSQLAndStatusMapping is the core regression: on the
// old code this fails at the UPDATE (syntax error near ORDER BY); with the
// fix it completes and maps every raw outcome to a CHECK-legal status while
// preserving the raw outcome.
func TestCompleteTask_ValidSQLAndStatusMapping(t *testing.T) {
	a, ctx, tenant := newTaskActivities(t)

	cases := []struct {
		rawOutcome string
		wantStatus string
	}{
		{"approved", "completed"},
		{"approve", "completed"},
		{"sign", "completed"},
		{"rejected", "rejected"},
		{"reject", "rejected"},
		{"decline", "rejected"},
		{"cancelled", "skipped"},
		{"escalate", "escalated"},
	}
	for _, tc := range cases {
		t.Run(tc.rawOutcome, func(t *testing.T) {
			instance := uuid.Must(uuid.NewV7()).String()
			seedPending(t, a, ctx, tenant, instance)

			// The call that used to fail with a Postgres parse error.
			require.NoError(t, a.CompleteTask(ctx, tenant, instance, 0, tc.rawOutcome, "note-"+tc.rawOutcome))

			var status, outcome, notes string
			var completedAt *time.Time
			require.NoError(t, a.Pool.QueryRow(ctx, `
				SELECT status, outcome, notes, completed_at FROM workflow_tasks
				WHERE tenant_id = $1 AND instance_id = $2`, tenant, instance).
				Scan(&status, &outcome, &notes, &completedAt))

			require.Equal(t, tc.wantStatus, status, "raw %q must map to a CHECK-legal status", tc.rawOutcome)
			require.Equal(t, tc.rawOutcome, outcome, "the raw outcome must be preserved verbatim")
			require.Equal(t, "note-"+tc.rawOutcome, notes)
			require.NotNil(t, completedAt, "completed_at must be stamped")
		})
	}
}

// TestCompleteTask_ParallelTasksEachCompleteDistinctRow pins the parallel
// review/signature case: several reviewers hold pending tasks for the SAME
// instance simultaneously. FOR UPDATE SKIP LOCKED must make each completion
// claim a DISTINCT pending row, so N decisions complete N tasks (never the
// same row N times, never 0). Runs the completions concurrently to actually
// exercise SKIP LOCKED.
func TestCompleteTask_ParallelTasksEachCompleteDistinctRow(t *testing.T) {
	a, ctx, tenant := newTaskActivities(t)
	instance := uuid.Must(uuid.NewV7()).String()

	const n = 4
	for i := 0; i < n; i++ {
		seedPending(t, a, ctx, tenant, instance)
	}

	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() { errs <- a.CompleteTask(ctx, tenant, instance, 0, "approved", "ok") }()
	}
	for i := 0; i < n; i++ {
		require.NoError(t, <-errs)
	}

	var pending, completed int
	require.NoError(t, a.Pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status='pending'),
		       count(*) FILTER (WHERE status='completed')
		FROM workflow_tasks WHERE tenant_id=$1 AND instance_id=$2`,
		tenant, instance).Scan(&pending, &completed))
	require.Equal(t, 0, pending, "every pending task must be claimed")
	require.Equal(t, n, completed, "each completion must claim a distinct row")
}
