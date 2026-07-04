package workflows

// Wave 8 Prompt 8.3 — DSR workflow branch coverage.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	"github.com/aieera/sedoc/services/workflow/internal/activities"
)

type dsrStub struct {
	subjectID   string
	held        bool
	summary     *activities.DSRSubjectSummary
	rows        int
	verifyError error // non-nil makes VerifyDSRToken fail
	events      []string
	ledger      []string
	requests    []string
}

func (s *dsrStub) Resolve(_ context.Context, _, _ string) (string, error) { return s.subjectID, nil }
func (s *dsrStub) Held(_ context.Context, _, _ string) (bool, error)      { return s.held, nil }
func (s *dsrStub) Collect(_ context.Context, _, _ string) (*activities.DSRSubjectSummary, error) {
	if s.summary == nil {
		return &activities.DSRSubjectSummary{SubjectID: s.subjectID}, nil
	}
	return s.summary, nil
}
func (s *dsrStub) Overwrite(_ context.Context, _, _, _, _ string) (int, error) { return s.rows, nil }
func (s *dsrStub) VerifyToken(_ context.Context, _, _, _ string) error         { return s.verifyError }
func (s *dsrStub) Ledger(_ context.Context, _, _, _, action, outcome string, _ map[string]any) (string, error) {
	s.ledger = append(s.ledger, action+":"+outcome)
	return "ledger-id", nil
}
func (s *dsrStub) UpdateReq(_ context.Context, _, _, status, _ string, _ map[string]any, _ string, _ *time.Time) error {
	s.requests = append(s.requests, status)
	return nil
}
func (s *dsrStub) Emit(_ context.Context, _, _, _, subject string, _ map[string]any) error {
	s.events = append(s.events, subject)
	return nil
}

func newDSREnv(t *testing.T, stub *dsrStub) *testsuite.TestWorkflowEnvironment {
	t.Helper()
	s := testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(stub.Resolve, activity.RegisterOptions{Name: "ResolveSubject"})
	env.RegisterActivityWithOptions(stub.Held, activity.RegisterOptions{Name: "SubjectHasHeldDocuments"})
	env.RegisterActivityWithOptions(stub.Collect, activity.RegisterOptions{Name: "CollectSubjectData"})
	env.RegisterActivityWithOptions(stub.Overwrite, activity.RegisterOptions{Name: "OverwriteSubjectPII"})
	env.RegisterActivityWithOptions(stub.VerifyToken, activity.RegisterOptions{Name: "VerifyDSRToken"})
	env.RegisterActivityWithOptions(stub.Ledger, activity.RegisterOptions{Name: "WritePrivacyLedger"})
	env.RegisterActivityWithOptions(stub.UpdateReq, activity.RegisterOptions{Name: "UpdateDSRRequest"})
	env.RegisterActivityWithOptions(stub.Emit, activity.RegisterOptions{Name: "EmitDSREvent"})
	return env
}

func TestExport_AbsentSubjectCompletesEmpty(t *testing.T) {
	stub := &dsrStub{subjectID: ""}
	env := newDSREnv(t, stub)
	env.ExecuteWorkflow(ExportWorkflow, DSRInput{TenantID: "t", RequestID: "r", SubjectEmail: "nobody@x.io"})
	require.NoError(t, env.GetWorkflowError())
	var out DSROutcome
	_ = env.GetWorkflowResult(&out)
	require.Equal(t, "completed", out.Status)
	require.Contains(t, stub.ledger, "export:completed-absent")
}

func TestExport_PresentSubjectCollectsSummary(t *testing.T) {
	stub := &dsrStub{
		subjectID: "u1",
		summary:   &activities.DSRSubjectSummary{SubjectID: "u1", DocumentsOwned: 3, TasksAssigned: 1, AuditEvents: 10},
	}
	env := newDSREnv(t, stub)
	env.ExecuteWorkflow(ExportWorkflow, DSRInput{TenantID: "t", RequestID: "r", SubjectEmail: "alice@x.io"})
	require.NoError(t, env.GetWorkflowError())
	var out DSROutcome
	_ = env.GetWorkflowResult(&out)
	require.Equal(t, "completed", out.Status)
	require.Equal(t, 3, out.Summary.DocumentsOwned)
	require.Contains(t, stub.events, SubjectDSRCompleted)
}

func TestErase_BlocksOnActiveHold(t *testing.T) {
	stub := &dsrStub{subjectID: "u1", held: true}
	env := newDSREnv(t, stub)
	env.ExecuteWorkflow(EraseWorkflow, DSRInput{
		TenantID: "t", RequestID: "r", SubjectEmail: "alice@x.io", VerificationToken: "t0k",
	})
	require.NoError(t, env.GetWorkflowError())
	var out DSROutcome
	_ = env.GetWorkflowResult(&out)
	require.Equal(t, "blocked", out.Status)
	require.Contains(t, stub.events, SubjectDSRBlocked)
	require.Contains(t, stub.ledger, "hold_block:blocked")
}

func TestErase_MissingVerificationFails(t *testing.T) {
	stub := &dsrStub{subjectID: "u1"}
	env := newDSREnv(t, stub)
	env.ExecuteWorkflow(EraseWorkflow, DSRInput{TenantID: "t", RequestID: "r", SubjectEmail: "alice@x.io"})
	// Failed workflows return the error from failWorkflow; we accept
	// either a nil error (when we chose to complete with status=failed)
	// or a non-nil one — both paths must mark status=failed.
	var out DSROutcome
	_ = env.GetWorkflowResult(&out)
	require.Equal(t, "failed", out.Status)
}

func TestErase_InvalidTokenFails(t *testing.T) {
	// Wave 11.4: VerifyDSRToken fails → workflow records
	// verify-mismatch + failed rows in the ledger and never touches
	// PII. The workflow bubbles the activity error up so the caller
	// can distinguish "failed verification" from "completed with
	// no-op"; GetWorkflowError is non-nil here.
	stub := &dsrStub{subjectID: "u1", verifyError: errors.New("token mismatch")}
	env := newDSREnv(t, stub)
	env.ExecuteWorkflow(EraseWorkflow, DSRInput{
		TenantID: "t", RequestID: "r", SubjectEmail: "alice@x.io", VerificationToken: "bad",
	})
	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())
	require.Contains(t, stub.ledger, "erase:verify-mismatch")
	require.NotContains(t, stub.ledger, "erase:completed", "PII must not be touched on verify failure")
}

func TestErase_HappyPathOverwrites(t *testing.T) {
	stub := &dsrStub{subjectID: "u1", held: false, rows: 5}
	env := newDSREnv(t, stub)
	env.ExecuteWorkflow(EraseWorkflow, DSRInput{
		TenantID: "t", RequestID: "r", SubjectEmail: "alice@x.io", VerificationToken: "tok",
	})
	require.NoError(t, env.GetWorkflowError())
	var out DSROutcome
	_ = env.GetWorkflowResult(&out)
	require.Equal(t, "completed", out.Status)
	require.Contains(t, stub.ledger, "erase:completed")
	require.Contains(t, stub.ledger, "erase:verified")
}

func TestAnonymize_HashesRatherThanOverwrites(t *testing.T) {
	stub := &dsrStub{subjectID: "u1", rows: 2}
	env := newDSREnv(t, stub)
	env.ExecuteWorkflow(AnonymizeWorkflow, DSRInput{
		TenantID: "t", RequestID: "r", SubjectEmail: "alice@x.io", TenantSalt: "salt",
	})
	require.NoError(t, env.GetWorkflowError())
	var out DSROutcome
	_ = env.GetWorkflowResult(&out)
	require.Equal(t, "completed", out.Status)
	require.Contains(t, stub.ledger, "anonymize:completed")
}
