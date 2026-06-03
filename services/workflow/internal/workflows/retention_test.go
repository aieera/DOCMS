package workflows

// Wave 8 Prompt 8.1 — RetentionWorkflow determinism + branch tests.
//
// Uses Temporal's TestWorkflowEnvironment with per-test activity stubs
// registered via activity.RegisterOptions{Name: ...} so the string
// lookups inside RetentionWorkflow ("SweepExpiredRetentions", etc.)
// match.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	"github.com/aieera/sedoc/services/workflow/internal/activities"
)

type retentionStub struct {
	sweep       []activities.ExpiredDocument
	holdByID    map[string]bool
	transitions []string // "docID->newState"
	events      []string // "subject:docID"
}

func (s *retentionStub) Sweep(_ context.Context, _ string, _ time.Time, _ int) ([]activities.ExpiredDocument, error) {
	return s.sweep, nil
}
func (s *retentionStub) Hold(_ context.Context, _ string, docID string) (bool, error) {
	return s.holdByID[docID], nil
}
func (s *retentionStub) Transition(_ context.Context, _ string, docID, newState, _ string) error {
	s.transitions = append(s.transitions, docID+"->"+newState)
	return nil
}
func (s *retentionStub) Emit(_ context.Context, _ string, docID, subject, _ string) error {
	s.events = append(s.events, subject+":"+docID)
	return nil
}

func newRetentionEnv(t *testing.T, stub *retentionStub) *testsuite.TestWorkflowEnvironment {
	t.Helper()
	s := testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(stub.Sweep, activity.RegisterOptions{Name: "SweepExpiredRetentions"})
	env.RegisterActivityWithOptions(stub.Hold, activity.RegisterOptions{Name: "DocumentOnLegalHold"})
	env.RegisterActivityWithOptions(stub.Transition, activity.RegisterOptions{Name: "RetentionTransition"})
	env.RegisterActivityWithOptions(stub.Emit, activity.RegisterOptions{Name: "EmitRetentionEvent"})
	return env
}

func TestRetention_NoExpiredDocs(t *testing.T) {
	stub := &retentionStub{}
	env := newRetentionEnv(t, stub)
	env.ExecuteWorkflow(RetentionWorkflow, RetentionInput{TenantID: "t1"})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var out RetentionOutcome
	require.NoError(t, env.GetWorkflowResult(&out))
	require.Equal(t, 0, out.Archived)
	require.Equal(t, 0, out.HeldSkipped)
}

func TestRetention_ActiveDocGetsArchived(t *testing.T) {
	stub := &retentionStub{
		sweep: []activities.ExpiredDocument{
			{DocumentID: "d1", LifecycleState: "active", RetentionUntil: time.Now().Add(-24 * time.Hour)},
		},
	}
	env := newRetentionEnv(t, stub)
	env.ExecuteWorkflow(RetentionWorkflow, RetentionInput{TenantID: "t1"})
	require.NoError(t, env.GetWorkflowError())
	var out RetentionOutcome
	_ = env.GetWorkflowResult(&out)
	require.Equal(t, 1, out.Archived)
	require.Contains(t, stub.transitions, "d1->archived")
}

func TestRetention_HeldDocIsSkippedAndEmits(t *testing.T) {
	stub := &retentionStub{
		sweep: []activities.ExpiredDocument{
			{DocumentID: "d1", LifecycleState: "active", RetentionUntil: time.Now().Add(-24 * time.Hour)},
		},
		holdByID: map[string]bool{"d1": true},
	}
	env := newRetentionEnv(t, stub)
	env.ExecuteWorkflow(RetentionWorkflow, RetentionInput{TenantID: "t1"})
	require.NoError(t, env.GetWorkflowError())
	var out RetentionOutcome
	_ = env.GetWorkflowResult(&out)
	require.Equal(t, 0, out.Archived)
	require.Equal(t, 1, out.HeldSkipped)
	require.Empty(t, stub.transitions)
	require.Contains(t, stub.events, SubjectRetentionHeld+":d1")
}

func TestRetention_OldArchivedBecomesDisposeCandidate(t *testing.T) {
	longAgo := time.Now().Add(-60 * 24 * time.Hour)
	stub := &retentionStub{
		sweep: []activities.ExpiredDocument{
			{DocumentID: "d1", LifecycleState: "archived", RetentionUntil: longAgo, UpdatedAt: longAgo},
		},
	}
	env := newRetentionEnv(t, stub)
	env.ExecuteWorkflow(RetentionWorkflow, RetentionInput{TenantID: "t1", ArchiveDays: 30})
	require.NoError(t, env.GetWorkflowError())
	var out RetentionOutcome
	_ = env.GetWorkflowResult(&out)
	require.Equal(t, 1, out.DisposeCandidates)
	require.Empty(t, stub.transitions)
	require.Contains(t, stub.events, SubjectRetentionDisposeCandidate+":d1")
}

func TestRetention_RecentlyArchivedStaysQuiet(t *testing.T) {
	recent := time.Now().Add(-5 * 24 * time.Hour)
	stub := &retentionStub{
		sweep: []activities.ExpiredDocument{
			{DocumentID: "d1", LifecycleState: "archived", RetentionUntil: recent, UpdatedAt: recent},
		},
	}
	env := newRetentionEnv(t, stub)
	env.ExecuteWorkflow(RetentionWorkflow, RetentionInput{TenantID: "t1", ArchiveDays: 30})
	require.NoError(t, env.GetWorkflowError())
	var out RetentionOutcome
	_ = env.GetWorkflowResult(&out)
	require.Equal(t, 0, out.DisposeCandidates)
	require.Empty(t, stub.events)
}

func TestRetention_DryRunTakesNoActions(t *testing.T) {
	stub := &retentionStub{
		sweep: []activities.ExpiredDocument{
			{DocumentID: "d1", LifecycleState: "active", RetentionUntil: time.Now().Add(-24 * time.Hour)},
		},
	}
	env := newRetentionEnv(t, stub)
	env.ExecuteWorkflow(RetentionWorkflow, RetentionInput{TenantID: "t1", DryRun: true})
	require.NoError(t, env.GetWorkflowError())
	var out RetentionOutcome
	_ = env.GetWorkflowResult(&out)
	require.Equal(t, 1, out.Archived)
	require.Empty(t, stub.transitions)
	require.Empty(t, stub.events)
}
