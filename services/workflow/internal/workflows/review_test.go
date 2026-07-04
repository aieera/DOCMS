package workflows

// Wave 7 Prompt 7.1 — ReviewWorkflow smoke + determinism tests.
//
// Full replay-history tests (DoD in final.md § 6.3) require exported
// event history from a real Temporal run and are Wave 7 Prompt 7.2
// territory. These tests pin the unit-level invariants:
//
//   - ReviewInput zero reviewers → immediate "approved".
//   - Reviewer decisions via signal → outcome matches the unanimity rule.
//   - Any reject short-circuits.
//
// Uses Temporal's in-memory TestWorkflowEnvironment so no external
// Temporal cluster is required.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

func TestReviewWorkflow_NoReviewersApprovesImmediately(t *testing.T) {
	s := testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()

	// No activities are expected when the reviewer list is empty.
	env.ExecuteWorkflow(ReviewWorkflow, ReviewInput{
		TenantID:   "t",
		InstanceID: "i",
		DocumentID: "d",
		VersionID:  "v",
		Reviewers:  nil,
	})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var outcome string
	require.NoError(t, env.GetWorkflowResult(&outcome))
	require.Equal(t, "approved", outcome)
}

// stubActivities satisfies the same method set as Activities but
// every method returns nil. Temporal requires the first argument be
// context.Context for an activity to register successfully.
type stubActivities struct{}

func (*stubActivities) CreateTask(_ context.Context, _, _, _, _, _ string) error {
	return nil
}
func (*stubActivities) NotifyAssignee(_ context.Context, _, _, _ string) error {
	return nil
}
func (*stubActivities) CompleteTask(_ context.Context, _, _ string, _ int, _, _ string) error {
	return nil
}
func (*stubActivities) DelegateTask(_ context.Context, _, _ string, _ int, _ string) error {
	return nil
}
func (*stubActivities) SetDocumentLifecycle(_ context.Context, _, _, _ string) error {
	return nil
}
func (*stubActivities) PublishEvent(_ context.Context, _, _ string, _ map[string]string) error {
	return nil
}
func (*stubActivities) EvaluateCondition(_ context.Context, _, _, _ string) (bool, error) {
	return false, nil
}

func TestReviewWorkflow_UnanimousApprove(t *testing.T) {
	s := testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterActivity(&stubActivities{})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(ReviewerDecidedSignal, ReviewerDecision{
			ReviewerID: "alice", Decision: "approve", Comment: "lgtm",
		})
	}, 1*time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(ReviewerDecidedSignal, ReviewerDecision{
			ReviewerID: "bob", Decision: "approve",
		})
	}, 2*time.Millisecond)

	env.ExecuteWorkflow(ReviewWorkflow, ReviewInput{
		TenantID: "t", InstanceID: "i", DocumentID: "d", VersionID: "v",
		Reviewers: []string{"alice", "bob"},
	})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var outcome string
	require.NoError(t, env.GetWorkflowResult(&outcome))
	require.Equal(t, "approved", outcome)
}

func TestReviewWorkflow_AnyRejectShortCircuits(t *testing.T) {
	s := testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterActivity(&stubActivities{})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(ReviewerDecidedSignal, ReviewerDecision{
			ReviewerID: "alice", Decision: "reject", Comment: "bad",
		})
	}, 1*time.Millisecond)

	env.ExecuteWorkflow(ReviewWorkflow, ReviewInput{
		TenantID: "t", InstanceID: "i", DocumentID: "d", VersionID: "v",
		Reviewers: []string{"alice", "bob"},
	})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var outcome string
	require.NoError(t, env.GetWorkflowResult(&outcome))
	require.Equal(t, "rejected", outcome)
}
