//go:build integration
// +build integration

// End-to-end proof that human-step workflows actually advance past their
// first task — the thing the CompleteTask parse error broke. These drive
// the REAL ApprovalWorkflow / ReviewWorkflow in Temporal's test env with
// the task-touching activities (CreateTask, CompleteTask) backed by a real
// Postgres, then assert the workflow_tasks rows transitioned to a
// CHECK-legal terminal status. All other activities are the existing
// no-op recorder stubs.
package workflows

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/workflow/internal/activities"
	"github.com/aieera/sedoc/services/workflow/internal/model"
)

// e2eTaskDDL — standalone workflow_tasks (document 000001 + 000005) with
// the exact status CHECK, no FKs (parents add nothing here). Same shape as
// the activities-package integration test.
const e2eTaskDDL = `
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

// dbTaskActivities is the recording stub with CreateTask + CompleteTask
// overridden to hit a real Postgres via the production Activities. The
// remaining 10 activities are promoted no-ops from *recordingActivities,
// so the workflow's full activity surface is registered.
type dbTaskActivities struct {
	*recordingActivities
	real *activities.Activities
}

func (d *dbTaskActivities) CreateTask(ctx context.Context, tenantID, instanceID, documentID, stepName, assignee string) error {
	return d.real.CreateTask(ctx, tenantID, instanceID, documentID, stepName, assignee)
}

func (d *dbTaskActivities) CompleteTask(ctx context.Context, tenantID, instanceID string, stepIndex int, outcome, notes string) error {
	// Record the raw outcome (as the base recorder does) AND persist.
	_ = d.recordingActivities.CompleteTask(ctx, tenantID, instanceID, stepIndex, outcome, notes)
	return d.real.CompleteTask(ctx, tenantID, instanceID, stepIndex, outcome, notes)
}

func newDBEnv(t *testing.T) (*testsuite.TestWorkflowEnvironment, *activities.Activities, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)

	dsn, cleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, e2eTaskDDL)
	require.NoError(t, err)

	real := &activities.Activities{Pool: pool, Log: zerolog.Nop()}
	s := testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterActivity(&dbTaskActivities{recordingActivities: newRecorder(), real: real})
	return env, real, ctx
}

func statusCounts(t *testing.T, a *activities.Activities, ctx context.Context, tenant, instance string) (pending, completed, rejected int) {
	t.Helper()
	require.NoError(t, a.Pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status='pending'),
		       count(*) FILTER (WHERE status='completed'),
		       count(*) FILTER (WHERE status='rejected')
		FROM workflow_tasks WHERE tenant_id=$1 AND instance_id=$2`,
		tenant, instance).Scan(&pending, &completed, &rejected))
	return
}

// TestApprovalWorkflow_E2E_TaskCompletes: a single-step approval creates a
// pending task, the approve signal completes it, and the task row lands in
// 'completed' (raw outcome 'approved' preserved) — the workflow proceeds to
// "approved" instead of stalling.
func TestApprovalWorkflow_E2E_TaskCompletes(t *testing.T) {
	env, real, ctx := newDBEnv(t)
	tenant := uuid.Must(uuid.NewV7()).String()
	instance := uuid.Must(uuid.NewV7()).String()
	doc := uuid.Must(uuid.NewV7()).String()
	assignee := uuid.Must(uuid.NewV7()).String()

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(StepCompletedSignal, model.StepSignal{Outcome: "approve", ActorID: assignee})
	}, time.Second)

	env.ExecuteWorkflow(ApprovalWorkflow, model.ApprovalInput{
		TenantID: tenant, InstanceID: instance, DocumentID: doc,
		Steps: []model.Step{{ID: "s1", Type: "approval", Name: "Legal", AssigneeID: assignee, SLAHours: 24}},
	})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var out string
	require.NoError(t, env.GetWorkflowResult(&out))
	require.Equal(t, "approved", out)

	pending, completed, _ := statusCounts(t, real, ctx, tenant, instance)
	require.Equal(t, 0, pending, "the approved task must not be left pending")
	require.Equal(t, 1, completed, "the task must transition to a CHECK-legal 'completed'")

	var outcome string
	require.NoError(t, real.Pool.QueryRow(ctx,
		`SELECT outcome FROM workflow_tasks WHERE tenant_id=$1 AND instance_id=$2`,
		tenant, instance).Scan(&outcome))
	require.Equal(t, "approved", outcome, "raw workflow outcome preserved for audit")
}

// TestReviewWorkflow_E2E_ParallelTasksAllComplete: three reviewers each get
// a pending task at once; three approve decisions must complete three
// DISTINCT rows (the SKIP LOCKED guarantee), leaving nothing pending — the
// parallel-review case the old code could neither run (parse error) nor
// target correctly.
func TestReviewWorkflow_E2E_ParallelTasksAllComplete(t *testing.T) {
	env, real, ctx := newDBEnv(t)
	tenant := uuid.Must(uuid.NewV7()).String()
	instance := uuid.Must(uuid.NewV7()).String()
	doc := uuid.Must(uuid.NewV7()).String()
	reviewers := []string{
		uuid.Must(uuid.NewV7()).String(),
		uuid.Must(uuid.NewV7()).String(),
		uuid.Must(uuid.NewV7()).String(),
	}

	for i, r := range reviewers {
		r := r
		env.RegisterDelayedCallback(func() {
			env.SignalWorkflow(ReviewerDecidedSignal, ReviewerDecision{ReviewerID: r, Decision: "approve"})
		}, time.Duration(i+1)*time.Second)
	}

	env.ExecuteWorkflow(ReviewWorkflow, ReviewInput{
		TenantID: tenant, InstanceID: instance, DocumentID: doc, VersionID: "v1",
		Reviewers: reviewers,
	})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var out string
	require.NoError(t, env.GetWorkflowResult(&out))
	require.Equal(t, "approved", out, "unanimous approval → approved")

	pending, completed, _ := statusCounts(t, real, ctx, tenant, instance)
	require.Equal(t, 0, pending, "no reviewer task may be left pending")
	require.Equal(t, len(reviewers), completed,
		"each of the N parallel decisions must complete a distinct task row")
}
