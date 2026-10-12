//go:build integration
// +build integration

// M-12: workflow recall was dead. The approver-acted gate called
// ListTasks(tenant, "", "") — binding "" to the UUID assignee_id column, an
// error on every call — and compared statuses ("approved") that the
// workflow_tasks CHECK never lets anyone store. Recall therefore always
// failed with a 500, before and after any approver acted.
package service

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// seedInstance inserts a running instance started by initiator with one
// task per status given, and returns the instance id.
func seedInstance(t *testing.T, tenant, initiator uuid.UUID, taskStatuses ...string) string {
	t.Helper()
	inst := uuid.Must(uuid.NewV7())
	_, err := testPool.Exec(t.Context(), `
		INSERT INTO workflow_instances (tenant_id, id, definition_id, status, current_step_id, temporal_workflow_id, started_by)
		VALUES ($1, $2, $3, 'running', '0', 'run-1', $4)`,
		tenant, inst, uuid.Must(uuid.NewV7()), initiator)
	require.NoError(t, err)
	for i, st := range taskStatuses {
		var completedAt any
		if st != "pending" {
			completedAt = time.Now().UTC()
		}
		_, err := testPool.Exec(t.Context(), `
			INSERT INTO workflow_tasks (tenant_id, instance_id, step_name, assignee_id, status, completed_at, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, now() + make_interval(secs => $7))`,
			tenant, inst, "step", uuid.Must(uuid.NewV7()), st, completedAt, i)
		require.NoError(t, err)
	}
	return inst.String()
}

func TestRecall_BeforeActionSucceeds(t *testing.T) {
	s, ft := newTestService(t)
	tenant, initiator, _ := newIDs()
	// Pending, delegated and escalated tasks are not decisions.
	inst := seedInstance(t, tenant, initiator, "pending", "delegated", "escalated")

	err := s.RecallInstance(as(tenant, initiator, "member"), tenant.String(), initiator.String(), inst)
	require.NoError(t, err, "recall before any approver acted must succeed")
	require.Len(t, ft.signals, 1, "recall must signal the workflow")
	require.Equal(t, "recall", ft.signals[0].Outcome)
	require.Equal(t, initiator.String(), ft.signals[0].ActorID)
}

func TestRecall_AfterApprovalRejected(t *testing.T) {
	// "completed" is what an approval is stored as, "rejected" a rejection
	// (activities.taskStatusFor); either settles the recall race.
	for _, decided := range []string{"completed", "rejected"} {
		t.Run(decided, func(t *testing.T) {
			s, ft := newTestService(t)
			tenant, initiator, _ := newIDs()
			inst := seedInstance(t, tenant, initiator, decided, "pending")

			err := s.RecallInstance(as(tenant, initiator, "member"), tenant.String(), initiator.String(), inst)
			require.Truef(t, errors.Is(err, ErrRecallTooLate), "err = %v, want ErrRecallTooLate", err)
			require.Empty(t, ft.signals, "a refused recall must not signal the workflow")
		})
	}
}

// Another instance's settled task never blocks this instance's recall.
func TestRecall_OtherInstanceDoesNotCount(t *testing.T) {
	s, _ := newTestService(t)
	tenant, initiator, _ := newIDs()
	seedInstance(t, tenant, initiator, "completed")
	inst := seedInstance(t, tenant, initiator, "pending")

	require.NoError(t, s.RecallInstance(as(tenant, initiator, "member"), tenant.String(), initiator.String(), inst))
}

func TestRecall_UnknownInstanceNotFound(t *testing.T) {
	s, _ := newTestService(t)
	tenant, initiator, _ := newIDs()
	err := s.RecallInstance(as(tenant, initiator, "member"), tenant.String(), initiator.String(), uuid.NewString())
	require.Truef(t, errors.Is(err, ErrNotFound), "err = %v, want ErrNotFound", err)
}
