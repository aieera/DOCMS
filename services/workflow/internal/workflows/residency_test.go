package workflows

// Wave 8 Prompt 8.4 — ResidencyMigrationWorkflow branch tests.
//
// Exercises the resume semantics: Next returns batches until empty,
// MoveDocumentRegion errors flip to MarkItemFailed, finalize stamps
// the terminal status.

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

type residencyStub struct {
	enqueued       int
	batches        [][]string
	callCount      int
	moveErrFor     map[string]bool
	movedIDs       []string
	failedIDs      []string
	finalizeStatus string
}

func (s *residencyStub) Enumerate(_ context.Context, _, _, _, _, _ string) (int, error) {
	return s.enqueued, nil
}
func (s *residencyStub) Next(_ context.Context, _, _ string, _ int) ([]string, error) {
	if s.callCount >= len(s.batches) {
		return nil, nil
	}
	batch := s.batches[s.callCount]
	s.callCount++
	return batch, nil
}
func (s *residencyStub) Move(_ context.Context, _, _, docID, _ string) error {
	if s.moveErrFor[docID] {
		return errors.New("move failed")
	}
	s.movedIDs = append(s.movedIDs, docID)
	return nil
}
func (s *residencyStub) MarkFailed(_ context.Context, _, _, docID, _ string) error {
	s.failedIDs = append(s.failedIDs, docID)
	return nil
}
func (s *residencyStub) Finalize(_ context.Context, _, _ string) (string, error) {
	return s.finalizeStatus, nil
}

func newResidencyEnv(t *testing.T, stub *residencyStub) *testsuite.TestWorkflowEnvironment {
	t.Helper()
	s := testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(stub.Enumerate, activity.RegisterOptions{Name: "EnumerateDocsForMigration"})
	env.RegisterActivityWithOptions(stub.Next, activity.RegisterOptions{Name: "NextPendingMigrationDoc"})
	env.RegisterActivityWithOptions(stub.Move, activity.RegisterOptions{Name: "MoveDocumentRegion"})
	env.RegisterActivityWithOptions(stub.MarkFailed, activity.RegisterOptions{Name: "MarkItemFailed"})
	env.RegisterActivityWithOptions(stub.Finalize, activity.RegisterOptions{Name: "FinalizeMigration"})
	return env
}

func TestResidency_EmptyEnumerationCompletes(t *testing.T) {
	stub := &residencyStub{finalizeStatus: "completed"}
	env := newResidencyEnv(t, stub)
	env.ExecuteWorkflow(ResidencyMigrationWorkflow, ResidencyMigrationInput{
		TenantID: "t", MigrationID: "m", SourceRegion: "us-east-1", TargetRegion: "eu-west-1",
	})
	require.NoError(t, env.GetWorkflowError())
	var out ResidencyMigrationOutcome
	_ = env.GetWorkflowResult(&out)
	require.Equal(t, "completed", out.Status)
	require.Equal(t, 0, out.Moved)
}

func TestResidency_MultiBatchMovesAll(t *testing.T) {
	stub := &residencyStub{
		enqueued:       3,
		batches:        [][]string{{"d1", "d2"}, {"d3"}},
		finalizeStatus: "completed",
	}
	env := newResidencyEnv(t, stub)
	env.ExecuteWorkflow(ResidencyMigrationWorkflow, ResidencyMigrationInput{
		TenantID: "t", MigrationID: "m", SourceRegion: "us-east-1", TargetRegion: "eu-west-1",
	})
	require.NoError(t, env.GetWorkflowError())
	var out ResidencyMigrationOutcome
	_ = env.GetWorkflowResult(&out)
	require.Equal(t, 3, out.Moved)
	require.Equal(t, 0, out.Failed)
	require.ElementsMatch(t, []string{"d1", "d2", "d3"}, stub.movedIDs)
}

func TestResidency_PerDocFailureIsIsolated(t *testing.T) {
	stub := &residencyStub{
		enqueued:       3,
		batches:        [][]string{{"d1", "d2", "d3"}},
		moveErrFor:     map[string]bool{"d2": true},
		finalizeStatus: "failed",
	}
	env := newResidencyEnv(t, stub)
	env.ExecuteWorkflow(ResidencyMigrationWorkflow, ResidencyMigrationInput{
		TenantID: "t", MigrationID: "m", SourceRegion: "a", TargetRegion: "b",
	})
	require.NoError(t, env.GetWorkflowError())
	var out ResidencyMigrationOutcome
	_ = env.GetWorkflowResult(&out)
	require.Equal(t, 2, out.Moved)
	require.Equal(t, 1, out.Failed)
	require.Equal(t, []string{"d2"}, stub.failedIDs)
	require.Equal(t, "failed", out.Status)
}
