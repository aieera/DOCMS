package workflows

// DEF-02 — the approval and review workflows when SetDocumentLifecycle
// refuses the change. The activity returns a NON-retryable application
// error typed "LegalHold" for a held document (and
// "InvalidLifecycleTransition" for a move the document state machine does
// not allow). The workflows must:
//
//   - not retry the refused activity (it ran exactly once);
//   - still complete with the reviewers' decision as the outcome;
//   - record a held document the way the retention cron does — a
//     dms.workflow.lifecycle_held.v1 event naming the document and the
//     state it was NOT moved to — and emit nothing extra for an invalid
//     transition.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"github.com/aieera/sedoc/services/workflow/internal/model"
)

type publishedEvent struct {
	subject string
	data    map[string]string
}

// lifecycleRecorder is recordingActivities with SetDocumentLifecycle
// returning a configurable error and PublishEvent recording its calls.
type lifecycleRecorder struct {
	*recordingActivities
	lmu          sync.Mutex
	lifecycleErr error
	lifecycle    []string // target states requested, one entry per attempt
	published    []publishedEvent
}

func (r *lifecycleRecorder) SetDocumentLifecycle(_ context.Context, _, _, state string) error {
	r.lmu.Lock()
	defer r.lmu.Unlock()
	r.lifecycle = append(r.lifecycle, state)
	return r.lifecycleErr
}

func (r *lifecycleRecorder) PublishEvent(_ context.Context, _, subject string, data map[string]string) error {
	r.lmu.Lock()
	defer r.lmu.Unlock()
	r.published = append(r.published, publishedEvent{subject: subject, data: data})
	return nil
}

func (r *lifecycleRecorder) subjects() []string {
	out := make([]string, 0, len(r.published))
	for _, p := range r.published {
		out = append(out, p.subject)
	}
	return out
}

func (r *lifecycleRecorder) find(subject string) (publishedEvent, bool) {
	for _, p := range r.published {
		if p.subject == subject {
			return p, true
		}
	}
	return publishedEvent{}, false
}

func heldErr() error {
	return temporal.NewNonRetryableApplicationError("document is under legal hold", "LegalHold", nil)
}

// newLifecycleEnv registers rec alone: its method set already includes the
// promoted recordingActivities methods, so registering both would collide.
func newLifecycleEnv(rec *lifecycleRecorder) *testsuite.TestWorkflowEnvironment {
	s := testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterActivity(rec)
	return env
}

func runApproval(t *testing.T, rec *lifecycleRecorder, outcome string, setup ...func(*testsuite.TestWorkflowEnvironment)) string {
	t.Helper()
	env := newLifecycleEnv(rec)
	for _, fn := range setup {
		fn(env)
	}
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(StepCompletedSignal, model.StepSignal{Outcome: outcome, ActorID: "user-A"})
	}, time.Second)
	env.ExecuteWorkflow(ApprovalWorkflow, model.ApprovalInput{
		TenantID: "t", InstanceID: "inst-1", DocumentID: "doc-1",
		Steps: []model.Step{{ID: "s1", Type: "approval", Name: "Legal", AssigneeID: "user-A", SLAHours: 24}},
	})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError(), "a refused lifecycle change must not fail the workflow")
	var out string
	require.NoError(t, env.GetWorkflowResult(&out))
	return out
}

func TestApproval_Approve_LegalHold_RecordsHeldAndCompletes(t *testing.T) {
	rec := &lifecycleRecorder{recordingActivities: newRecorder(), lifecycleErr: heldErr()}

	out := runApproval(t, rec, "approve")

	require.Equal(t, "approved", out)
	require.Equal(t, []string{"active"}, rec.lifecycle, "LegalHold is non-retryable: exactly one attempt")
	held, ok := rec.find("dms.workflow.lifecycle_held.v1")
	require.True(t, ok, "held document must be recorded; got %v", rec.subjects())
	require.Equal(t, "inst-1", held.data["instance_id"])
	require.Equal(t, "doc-1", held.data["document_id"])
	require.Equal(t, "active", held.data["target_state"])
	require.NotEmpty(t, held.data["reason"])
}

func TestApproval_Reject_LegalHold_RecordsHeldAndCompletes(t *testing.T) {
	rec := &lifecycleRecorder{recordingActivities: newRecorder(), lifecycleErr: heldErr()}

	out := runApproval(t, rec, "reject")

	require.Equal(t, "rejected", out)
	require.Equal(t, []string{"draft"}, rec.lifecycle)
	held, ok := rec.find("dms.workflow.lifecycle_held.v1")
	require.True(t, ok, "held document must be recorded; got %v", rec.subjects())
	require.Equal(t, "draft", held.data["target_state"])
}

func TestApproval_InvalidTransition_CompletesWithoutHeldEvent(t *testing.T) {
	rec := &lifecycleRecorder{
		recordingActivities: newRecorder(),
		lifecycleErr: temporal.NewNonRetryableApplicationError(
			"invalid transition", "InvalidLifecycleTransition", nil),
	}

	out := runApproval(t, rec, "approve")

	require.Equal(t, "approved", out)
	require.Equal(t, []string{"active"}, rec.lifecycle, "InvalidLifecycleTransition is non-retryable")
	_, held := rec.find("dms.workflow.lifecycle_held.v1")
	require.False(t, held, "an invalid transition is not a legal hold")
}

func TestApproval_LifecycleApplied_NoHeldEvent(t *testing.T) {
	rec := &lifecycleRecorder{recordingActivities: newRecorder()}

	out := runApproval(t, rec, "approve")

	require.Equal(t, "approved", out)
	require.Equal(t, []string{"active"}, rec.lifecycle)
	require.Equal(t, []string{"dms.workflow.completed.v1"}, rec.subjects())
}

func TestReview_Approve_LegalHold_RecordsHeldAndCompletes(t *testing.T) {
	rec := &lifecycleRecorder{recordingActivities: newRecorder(), lifecycleErr: heldErr()}
	env := newLifecycleEnv(rec)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(ReviewerDecidedSignal, ReviewerDecision{ReviewerID: "alice", Decision: "approve"})
	}, time.Millisecond)

	env.ExecuteWorkflow(ReviewWorkflow, ReviewInput{
		TenantID: "t", InstanceID: "inst-2", DocumentID: "doc-2", VersionID: "v",
		Reviewers: []string{"alice"},
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var out string
	require.NoError(t, env.GetWorkflowResult(&out))
	require.Equal(t, "approved", out)
	require.Equal(t, []string{"active"}, rec.lifecycle)
	held, ok := rec.find("dms.workflow.lifecycle_held.v1")
	require.True(t, ok, "held document must be recorded; got %v", rec.subjects())
	require.Equal(t, "doc-2", held.data["document_id"])
	require.Equal(t, "active", held.data["target_state"])
}

// ---- Temporal versioning (review M-2) ------------------------------------
//
// The lifecycle_held PublishEvent is a command the pre-DEF-02 workflow code
// never issued: it discarded SetDocumentLifecycle's error and went straight
// on (approve → PublishEvent completed; reject → complete the workflow). It
// is gated with workflow.GetVersion(lifecycleHeldChangeID), so a history
// recorded before the change — no version marker — replays with
// DefaultVersion on the old path instead of hitting a nondeterminism error
// (reject) or silently swallowing the completed event (approve).

// preVersion makes GetVersion report DefaultVersion, as on replay of a
// history recorded before the change.
func preVersion(env *testsuite.TestWorkflowEnvironment) {
	env.OnGetVersion(lifecycleHeldChangeID, workflow.DefaultVersion, 1).Return(workflow.DefaultVersion)
}

func TestApproval_Approve_LegalHold_PreVersion_OldPath(t *testing.T) {
	rec := &lifecycleRecorder{recordingActivities: newRecorder(), lifecycleErr: heldErr()}

	out := runApproval(t, rec, "approve", preVersion)

	require.Equal(t, "approved", out)
	require.Equal(t, []string{"active"}, rec.lifecycle)
	require.Equal(t, []string{"dms.workflow.completed.v1"}, rec.subjects(),
		"pre-change history: no lifecycle_held command before the completed event")
}

func TestApproval_Reject_LegalHold_PreVersion_OldPath(t *testing.T) {
	rec := &lifecycleRecorder{recordingActivities: newRecorder(), lifecycleErr: heldErr()}

	out := runApproval(t, rec, "reject", preVersion)

	require.Equal(t, "rejected", out)
	require.Equal(t, []string{"draft"}, rec.lifecycle)
	require.Empty(t, rec.subjects(), "pre-change history: the workflow completes right after SetDocumentLifecycle")
}

func TestApproval_Approve_LegalHold_Version1_RecordsHeld(t *testing.T) {
	rec := &lifecycleRecorder{recordingActivities: newRecorder(), lifecycleErr: heldErr()}

	out := runApproval(t, rec, "approve", func(env *testsuite.TestWorkflowEnvironment) {
		env.OnGetVersion(lifecycleHeldChangeID, workflow.DefaultVersion, 1).Return(workflow.Version(1))
	})

	require.Equal(t, "approved", out)
	require.Equal(t, []string{"dms.workflow.lifecycle_held.v1", "dms.workflow.completed.v1"}, rec.subjects())
}

func TestReview_Approve_LegalHold_PreVersion_OldPath(t *testing.T) {
	rec := &lifecycleRecorder{recordingActivities: newRecorder(), lifecycleErr: heldErr()}
	env := newLifecycleEnv(rec)
	preVersion(env)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(ReviewerDecidedSignal, ReviewerDecision{ReviewerID: "alice", Decision: "approve"})
	}, time.Millisecond)

	env.ExecuteWorkflow(ReviewWorkflow, ReviewInput{
		TenantID: "t", InstanceID: "inst-3", DocumentID: "doc-3", VersionID: "v",
		Reviewers: []string{"alice"},
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	_, held := rec.find("dms.workflow.lifecycle_held.v1")
	require.False(t, held, "pre-change history: no lifecycle_held command; got %v", rec.subjects())
	_, approved := rec.find("dms.review.approved.v1")
	require.True(t, approved, "the review's own event still follows; got %v", rec.subjects())
}
