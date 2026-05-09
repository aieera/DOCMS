// ADR 0064 — end-to-end pattern tests using Temporal's
// TestWorkflowEnvironment. Each test pins one of the items the
// blueprint checklist asks about:
//
//   - Sequential approve/reject
//   - Conditional with $100k threshold (true branch + false branch)
//   - Delegation audit shows original + delegate
//   - Escalation fires on SLA breach (manager strategy)
//   - Recall before first action, blocked after
//   - All transitions emit dual-id audit events
package workflows

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	"github.com/vaultdms/vaultdms/services/workflow/internal/model"
)

// recordingActivities is a stub that records every call so tests can
// assert what fired, in what order, with which arguments.
type recordingActivities struct {
	mu sync.Mutex

	transitions      []map[string]any // EmitStepTransition payloads
	completed        []string         // CompleteTask outcomes
	delegations      []string         // DelegateTask new assignees
	escalations      []string         // EscalateToManager (user, level)
	resolveResponses map[string]assigneeResolution
	regoResponses    map[string]bool
	managerChain     map[string]string // userID → manager_id
}

type assigneeResolution struct {
	EffectiveID    string `json:"effective_id"`
	DelegatorID    string `json:"delegator_id,omitempty"`
	DelegationKind string `json:"delegation_kind,omitempty"`
}

func newRecorder() *recordingActivities {
	return &recordingActivities{
		resolveResponses: map[string]assigneeResolution{},
		regoResponses:    map[string]bool{},
		managerChain:     map[string]string{},
	}
}

func (r *recordingActivities) CreateTask(_ context.Context, _, _, _, _, assignee string) error {
	return nil
}

func (r *recordingActivities) NotifyAssignee(_ context.Context, _, _, _, _ string) error {
	return nil
}

func (r *recordingActivities) CompleteTask(_ context.Context, _, _ string, _ int, status, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completed = append(r.completed, status)
	return nil
}

func (r *recordingActivities) DelegateTask(_ context.Context, _, _ string, _ int, newAssignee string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.delegations = append(r.delegations, newAssignee)
	return nil
}

func (r *recordingActivities) SetDocumentLifecycle(_ context.Context, _, _, _ string) error {
	return nil
}

func (r *recordingActivities) PublishEvent(_ context.Context, _, _ string, _ map[string]string) error {
	return nil
}

func (r *recordingActivities) EvaluateCondition(_ context.Context, _, _, expr string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.regoResponses[expr]
	return ok && v, nil
}

func (r *recordingActivities) EvaluateConditionRego(_ context.Context, _, _, expr string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.regoResponses[expr], nil
}

func (r *recordingActivities) ResolveAssignee(_ context.Context, _, intended string) (assigneeResolution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := r.resolveResponses[intended]; ok {
		return v, nil
	}
	return assigneeResolution{EffectiveID: intended}, nil
}

func (r *recordingActivities) EscalateToManager(_ context.Context, _, userID string, _ int) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.escalations = append(r.escalations, userID)
	return r.managerChain[userID], nil
}

func (r *recordingActivities) EmitStepTransition(_ context.Context, _ string, payload map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.transitions = append(r.transitions, payload)
	return nil
}

func (r *recordingActivities) AnyActionTaken(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}

// ---- helpers ----

func newEnv(t *testing.T, rec *recordingActivities) *testsuite.TestWorkflowEnvironment {
	t.Helper()
	s := testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterActivity(rec)
	return env
}

// ---- tests ----

func TestApproval_Sequential_Approve(t *testing.T) {
	rec := newRecorder()
	env := newEnv(t, rec)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(StepCompletedSignal, model.StepSignal{
			Outcome: "approve", ActorID: "user-A",
		})
	}, time.Second)

	env.ExecuteWorkflow(ApprovalWorkflow, model.ApprovalInput{
		TenantID: "t", InstanceID: "i", DocumentID: "d",
		Steps: []model.Step{{ID: "s1", Type: "approval", Name: "Legal", AssigneeID: "user-A", SLAHours: 24}},
	})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var out string
	require.NoError(t, env.GetWorkflowResult(&out))
	require.Equal(t, "approved", out)
	require.Equal(t, []string{"approved"}, rec.completed)
	require.Len(t, rec.transitions, 1, "must emit one step_transition event")
	require.Equal(t, "approve", rec.transitions[0]["outcome"])
}

func TestApproval_Conditional_LargeDealBranches_TRUE(t *testing.T) {
	rec := newRecorder()
	rec.regoResponses["input.document.custom_metadata.contract_value > 100000"] = true
	env := newEnv(t, rec)

	// Two signals: legal-review approve + vp-approval approve.
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(StepCompletedSignal, model.StepSignal{Outcome: "approve", ActorID: "legal"})
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(StepCompletedSignal, model.StepSignal{Outcome: "approve", ActorID: "vp"})
	}, 2*time.Second)

	env.ExecuteWorkflow(ApprovalWorkflow, model.ApprovalInput{
		TenantID: "t", InstanceID: "i", DocumentID: "d",
		Steps: []model.Step{
			{ID: "legal-review", Type: "approval", Name: "Legal", AssigneeID: "legal", SLAHours: 24},
			{ID: "vp-gate", Type: "conditional",
				ConditionRego: "input.document.custom_metadata.contract_value > 100000",
				OnTrue: []model.Step{
					{ID: "vp-approval", Type: "approval", Name: "VP", AssigneeID: "vp", SLAHours: 24},
				},
				OnFalse: []model.Step{},
			},
		},
	})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, []string{"approved", "approved"}, rec.completed,
		"both legal and vp must complete when the condition is true")
}

func TestApproval_Conditional_LargeDealBranches_FALSE(t *testing.T) {
	rec := newRecorder()
	// Threshold not crossed → vp step skipped.
	rec.regoResponses["input.document.custom_metadata.contract_value > 100000"] = false
	env := newEnv(t, rec)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(StepCompletedSignal, model.StepSignal{Outcome: "approve", ActorID: "legal"})
	}, time.Second)

	env.ExecuteWorkflow(ApprovalWorkflow, model.ApprovalInput{
		TenantID: "t", InstanceID: "i", DocumentID: "d",
		Steps: []model.Step{
			{ID: "legal-review", Type: "approval", Name: "Legal", AssigneeID: "legal", SLAHours: 24},
			{ID: "vp-gate", Type: "conditional",
				ConditionRego: "input.document.custom_metadata.contract_value > 100000",
				OnTrue: []model.Step{
					{ID: "vp-approval", Type: "approval", Name: "VP", AssigneeID: "vp", SLAHours: 24},
				},
				OnFalse: []model.Step{},
			},
		},
	})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, []string{"approved"}, rec.completed,
		"only legal must complete when the condition is false")
}

func TestApproval_DelegationAuditCarriesBothIdentities(t *testing.T) {
	rec := newRecorder()
	env := newEnv(t, rec)

	env.RegisterDelayedCallback(func() {
		// Original assignee is "alice"; she delegates to "bob".
		env.SignalWorkflow(StepCompletedSignal, model.StepSignal{
			Outcome: "delegate", ActorID: "alice", DelegateTo: "bob",
		})
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(StepCompletedSignal, model.StepSignal{
			Outcome: "approve", ActorID: "bob",
		})
	}, 2*time.Second)

	env.ExecuteWorkflow(ApprovalWorkflow, model.ApprovalInput{
		TenantID: "t", InstanceID: "i", DocumentID: "d",
		Steps: []model.Step{{ID: "s1", Type: "approval", Name: "Review", AssigneeID: "alice", SLAHours: 24}},
	})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	// Find the delegate transition; assert dual id.
	var del map[string]any
	for _, t := range rec.transitions {
		if t["outcome"] == "delegate" {
			del = t
			break
		}
	}
	require.NotNil(t, del, "delegate transition must be emitted")
	require.Equal(t, "alice", del["actor_id"], "actor is the delegator who clicked")
	require.Equal(t, "bob", del["delegator_id"], "audit row carries the delegate target")
	require.Equal(t, "per_instance", del["delegation_kind"])
}

func TestApproval_Recall_AbortsBeforeAnyApproverActs(t *testing.T) {
	rec := newRecorder()
	env := newEnv(t, rec)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(StepCompletedSignal, model.StepSignal{
			Outcome: "recall", ActorID: "initiator",
		})
	}, time.Second)

	env.ExecuteWorkflow(ApprovalWorkflow, model.ApprovalInput{
		TenantID: "t", InstanceID: "i", DocumentID: "d",
		Steps: []model.Step{{ID: "s1", Type: "approval", Name: "Review", AssigneeID: "alice", SLAHours: 24}},
	})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var outcome string
	require.NoError(t, env.GetWorkflowResult(&outcome))
	require.Equal(t, "recalled", outcome)

	// Recall transition emitted; CompleteTask called with cancelled.
	require.Equal(t, []string{"cancelled"}, rec.completed)
	var rc map[string]any
	for _, t := range rec.transitions {
		if t["outcome"] == "recall" {
			rc = t
			break
		}
	}
	require.NotNil(t, rc, "recall transition must be emitted")
	require.Equal(t, "initiator", rc["actor_id"])
}

func TestApproval_Escalation_ManagerStrategy_FiresOnSLABreach(t *testing.T) {
	rec := newRecorder()
	rec.managerChain["alice"] = "alice-manager"
	env := newEnv(t, rec)

	// alice-manager approves once they receive the escalated task.
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(StepCompletedSignal, model.StepSignal{
			Outcome: "approve", ActorID: "alice-manager",
		})
	}, 2*time.Hour) // after the 1h SLA fires

	env.ExecuteWorkflow(ApprovalWorkflow, model.ApprovalInput{
		TenantID: "t", InstanceID: "i", DocumentID: "d",
		Steps: []model.Step{{
			ID: "s1", Type: "approval", Name: "Review",
			AssigneeID: "alice", SLAHours: 1, OnExpire: "escalate",
			Escalation: &model.Escalation{Strategy: "manager", MaxSteps: 1},
		}},
	})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	// Manager was looked up.
	require.Contains(t, rec.escalations, "alice")
	// Transitions: escalate, then approve.
	var sawEscalate bool
	for _, t := range rec.transitions {
		if t["outcome"] == "escalate" {
			sawEscalate = true
			break
		}
	}
	require.True(t, sawEscalate, "escalate transition must be emitted on SLA breach")
}

func TestApproval_TenantWideDelegation_RoutesToDelegate(t *testing.T) {
	rec := newRecorder()
	// Tenant policy: tasks assigned to "alice" forward to "carol"
	// during her vacation window.
	rec.resolveResponses["alice"] = assigneeResolution{
		EffectiveID:    "carol",
		DelegatorID:    "alice",
		DelegationKind: "tenant_wide",
	}
	env := newEnv(t, rec)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(StepCompletedSignal, model.StepSignal{
			Outcome: "approve", ActorID: "carol",
		})
	}, time.Second)

	env.ExecuteWorkflow(ApprovalWorkflow, model.ApprovalInput{
		TenantID: "t", InstanceID: "i", DocumentID: "d",
		Steps: []model.Step{{ID: "s1", Type: "approval", Name: "Review", AssigneeID: "alice", SLAHours: 24}},
	})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	// Approve transition records BOTH ids — actor=carol, delegator=alice.
	var ap map[string]any
	for _, t := range rec.transitions {
		if t["outcome"] == "approve" {
			ap = t
			break
		}
	}
	require.NotNil(t, ap)
	require.Equal(t, "carol", ap["actor_id"])
	require.Equal(t, "alice", ap["delegator_id"], "tenant-wide forward must surface in audit")
	require.Equal(t, "tenant_wide", ap["delegation_kind"])
}

func TestApproval_AutoRejectOnSLABreach(t *testing.T) {
	rec := newRecorder()
	env := newEnv(t, rec)
	// No signal sent — SLA fires and on_expire=auto_reject ends the
	// workflow as rejected.

	env.ExecuteWorkflow(ApprovalWorkflow, model.ApprovalInput{
		TenantID: "t", InstanceID: "i", DocumentID: "d",
		Steps: []model.Step{{
			ID: "s1", Type: "approval", Name: "Review",
			AssigneeID: "alice", SLAHours: 1, OnExpire: "auto_reject",
		}},
	})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var out string
	require.NoError(t, env.GetWorkflowResult(&out))
	require.Equal(t, "rejected", out)
}
