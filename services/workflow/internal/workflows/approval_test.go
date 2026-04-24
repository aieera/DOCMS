package workflows

// ApprovalWorkflow smoke + determinism tests.
//
// Pin the unit-level invariants the brief calls out:
//   - 5-step chain where every approver approves → outcome "approved".
//   - First reject short-circuits the chain (no later steps notified).
//   - On-leave assignee is auto-skipped without waiting for a signal,
//     and the chain proceeds to the next step.
//
// Full event-history replay test (chaos kill mid-chain) needs an
// exported history file from a real Temporal cluster; same deferral
// as review_test.go's header. These in-memory tests are sufficient
// to catch determinism / signal-routing regressions.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	"github.com/vaultdms/vaultdms/services/workflow/internal/model"
)

func mkSteps(n int) []model.Step {
	out := make([]model.Step, n)
	for i := 0; i < n; i++ {
		out[i] = model.Step{
			Name:         "step-" + string(rune('A'+i)),
			Type:         "approval",
			AssigneeID:   "user-" + string(rune('A'+i)),
			TimeoutHours: 72,
		}
	}
	return out
}

// All five approve → outcome "approved".
func TestApprovalWorkflow_AllApproveCompletesApproved(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.RegisterActivity(&stubActivities{})

	for i := 0; i < 5; i++ {
		i := i
		env.RegisterDelayedCallback(func() {
			env.SignalWorkflow(StepCompletedSignal, model.StepSignal{
				StepIndex: i, Outcome: "approve", ActorID: "user-X",
			})
		}, time.Duration(i+1)*time.Second)
	}

	env.ExecuteWorkflow(ApprovalWorkflow, model.ApprovalInput{
		TenantID: "t", InstanceID: "i", DocumentID: "d", InitiatedBy: "u",
		Steps: mkSteps(5),
	})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var outcome string
	require.NoError(t, env.GetWorkflowResult(&outcome))
	require.Equal(t, "approved", outcome)
}

// First step rejects → outcome "rejected"; later steps must not be notified.
// Counts NotifyAssignee invocations via a tracking stub registered
// alongside the default stubActivities.
type notifyTrackingStub struct{ stubActivities; calls atomic.Int64 }

func (s *notifyTrackingStub) NotifyAssignee(_ context.Context, _, _, _, _ string) error {
	s.calls.Add(1)
	return nil
}

func TestApprovalWorkflow_FirstRejectShortCircuits(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	stub := &notifyTrackingStub{}
	env.RegisterActivity(stub)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(StepCompletedSignal, model.StepSignal{
			StepIndex: 0, Outcome: "reject", ActorID: "user-A", Notes: "no",
		})
	}, time.Second)

	env.ExecuteWorkflow(ApprovalWorkflow, model.ApprovalInput{
		TenantID: "t", InstanceID: "i", DocumentID: "d", InitiatedBy: "u",
		Steps: mkSteps(5),
	})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var outcome string
	require.NoError(t, env.GetWorkflowResult(&outcome))
	require.Equal(t, "rejected", outcome)
	require.Equal(t, int64(1), stub.calls.Load(), "later steps must not be notified after a reject")
}

// On-leave assignee on step 2 → step is auto-skipped (no NotifyAssignee
// for that step), workflow proceeds and ends approved.
func TestApprovalWorkflow_OnLeaveSkipsStep(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.RegisterActivity(&stubActivities{})

	// Override IsUserOnLeave with a per-assignee version: user-B is on leave,
	// everyone else is available. RegisterActivityWithOptions wins over the
	// stub registration above for this activity name.
	leaveCheck := func(_ context.Context, assigneeID string) (stubAvailability, error) {
		if assigneeID == "user-B" {
			return stubAvailability{OnLeave: true, Reason: "vacation"}, nil
		}
		return stubAvailability{}, nil
	}
	env.RegisterActivityWithOptions(leaveCheck, activity.RegisterOptions{
		Name: "IsUserOnLeave", DisableAlreadyRegisteredCheck: true,
	})

	// Track who actually got notified.
	notifiedTo := map[string]bool{}
	notify := func(_ context.Context, _ string, assigneeID string, _, _ string) error {
		notifiedTo[assigneeID] = true
		return nil
	}
	env.RegisterActivityWithOptions(notify, activity.RegisterOptions{
		Name: "NotifyAssignee", DisableAlreadyRegisteredCheck: true,
	})

	// Two non-skipped steps (A and C) need approve signals. B is skipped
	// without consuming a signal. StepIndex still maps to position in
	// the original list.
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(StepCompletedSignal, model.StepSignal{StepIndex: 0, Outcome: "approve"})
	}, 1*time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(StepCompletedSignal, model.StepSignal{StepIndex: 2, Outcome: "approve"})
	}, 2*time.Second)

	env.ExecuteWorkflow(ApprovalWorkflow, model.ApprovalInput{
		TenantID: "t", InstanceID: "i", DocumentID: "d", InitiatedBy: "u",
		Steps: mkSteps(3),
	})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var outcome string
	require.NoError(t, env.GetWorkflowResult(&outcome))
	require.Equal(t, "approved", outcome)
	require.False(t, notifiedTo["user-B"], "on-leave assignee must not be notified")
	require.True(t, notifiedTo["user-A"], "available assignees must be notified")
	require.True(t, notifiedTo["user-C"], "available assignees must be notified")
}
