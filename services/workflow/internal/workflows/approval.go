// Package workflows contains Temporal workflow definitions.
package workflows

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/vaultdms/vaultdms/services/workflow/internal/model"
)

const StepCompletedSignal = "step_completed"

// ApprovalWorkflow executes sequential approval steps. Each step waits for
// a signal or timeout. On reject the document reverts to draft. On full
// approval the document transitions to active.
func ApprovalWorkflow(ctx workflow.Context, input model.ApprovalInput) (string, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("approval workflow started", "document", input.DocumentID, "steps", len(input.Steps))

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	for i, step := range input.Steps {
		// ADR 0064 — both `condition` (legacy) and `conditional` (new
		// schema type) take the conditional branch. ConditionRego (new)
		// uses OPA Rego; legacy Condition uses expr-lang.
		if step.Type == "condition" || step.Type == "conditional" {
			condResult, err := evaluateCondition(ctx, input, step)
			if err != nil {
				return "error", err
			}
			branch := step.OnFalse
			if condResult {
				branch = step.OnTrue
			}
			for _, s := range branch {
				outcome := executeStep(ctx, input, i, s)
				if outcome == "rejected" {
					return "rejected", nil
				}
				if outcome == "recalled" {
					_ = workflow.ExecuteActivity(ctx, "SetDocumentLifecycle", input.TenantID, input.DocumentID, "draft").Get(ctx, nil)
					return "recalled", nil
				}
			}
			continue
		}

		outcome := executeStep(ctx, input, i, step)
		if outcome == "rejected" {
			_ = workflow.ExecuteActivity(ctx, "SetDocumentLifecycle", input.TenantID, input.DocumentID, "draft").Get(ctx, nil)
			return "rejected", nil
		}
		if outcome == "recalled" {
			_ = workflow.ExecuteActivity(ctx, "SetDocumentLifecycle", input.TenantID, input.DocumentID, "draft").Get(ctx, nil)
			_ = workflow.ExecuteActivity(ctx, "PublishEvent", input.TenantID, "dms.workflow.recalled.v1", map[string]string{
				"instance_id": input.InstanceID, "document_id": input.DocumentID,
			}).Get(ctx, nil)
			return "recalled", nil
		}
	}

	_ = workflow.ExecuteActivity(ctx, "SetDocumentLifecycle", input.TenantID, input.DocumentID, "active").Get(ctx, nil)
	_ = workflow.ExecuteActivity(ctx, "PublishEvent", input.TenantID, "dms.workflow.completed.v1", map[string]string{
		"instance_id": input.InstanceID, "document_id": input.DocumentID, "outcome": "approved",
	}).Get(ctx, nil)
	return "approved", nil
}

func executeStep(ctx workflow.Context, input model.ApprovalInput, idx int, step model.Step) string {
	logger := workflow.GetLogger(ctx)

	// ADR 0064 — resolve tenant-wide forwarding before creating the
	// task. ResolveAssignee returns the original id unchanged when no
	// active workflow_delegations row applies; otherwise the
	// delegate's id, plus the original delegator id for audit.
	intended := step.AssigneeID
	effective := intended
	delegatorOnAssign := ""
	delegationKindOnAssign := ""
	if intended != "" {
		var res activitiesAssigneeResolution
		if err := workflow.ExecuteActivity(ctx, "ResolveAssignee", input.TenantID, intended).Get(ctx, &res); err == nil && res.EffectiveID != "" {
			effective = res.EffectiveID
			delegatorOnAssign = res.DelegatorID
			delegationKindOnAssign = res.DelegationKind
		}
	}

	_ = workflow.ExecuteActivity(ctx, "CreateTask", input.TenantID, input.InstanceID, input.DocumentID, step.Name, effective).Get(ctx, nil)
	_ = workflow.ExecuteActivity(ctx, "NotifyAssignee", input.TenantID, effective, input.DocumentID, step.Name).Get(ctx, nil)

	// SLAHours is the ADR 0064 field; TimeoutHours is the legacy one.
	// Either populates the timer; SLAHours wins when both set.
	timeoutHours := step.SLAHours
	if timeoutHours == 0 {
		timeoutHours = step.TimeoutHours
	}
	timeout := time.Duration(timeoutHours) * time.Hour
	if timeout <= 0 {
		timeout = 72 * time.Hour
	}

	signalCh := workflow.GetSignalChannel(ctx, StepCompletedSignal)
	timerCtx, timerCancel := workflow.WithCancel(ctx)
	timerFuture := workflow.NewTimer(timerCtx, timeout)

	var signal model.StepSignal
	sel := workflow.NewSelector(ctx)
	sel.AddReceive(signalCh, func(ch workflow.ReceiveChannel, _ bool) {
		ch.Receive(ctx, &signal)
		timerCancel()
	})
	sel.AddFuture(timerFuture, func(f workflow.Future) {
		logger.Info("step timeout", "step", step.Name)
		// Translate SLA expiry into the configured outcome. on_expire
		// auto_approve / auto_reject lets a tenant route around an
		// unresponsive approver without involving a human.
		outcome := "escalate"
		switch step.OnExpire {
		case "auto_approve":
			outcome = "approve"
		case "auto_reject":
			outcome = "reject"
		}
		signal = model.StepSignal{StepIndex: idx, Outcome: outcome}
	})
	sel.Select(ctx)

	stepID := step.ID
	if stepID == "" {
		stepID = step.Name
	}

	switch signal.Outcome {
	case "approve":
		_ = workflow.ExecuteActivity(ctx, "CompleteTask", input.TenantID, input.InstanceID, idx, "approved", signal.Notes).Get(ctx, nil)
		emitTransition(ctx, input, stepID, "approve", signal.ActorID, delegatorOnAssign, delegationKindOnAssign, idx, idx+1)
		return "approved"
	case "reject":
		_ = workflow.ExecuteActivity(ctx, "CompleteTask", input.TenantID, input.InstanceID, idx, "rejected", signal.Notes).Get(ctx, nil)
		emitTransition(ctx, input, stepID, "reject", signal.ActorID, delegatorOnAssign, delegationKindOnAssign, idx, idx)
		return "rejected"
	case "delegate":
		_ = workflow.ExecuteActivity(ctx, "DelegateTask", input.TenantID, input.InstanceID, idx, signal.DelegateTo).Get(ctx, nil)
		// Per-instance delegation. Audit row carries actor (the
		// delegator who clicked) and delegate (the target). Both
		// identities are visible in the audit trail per ADR 0064.
		emitTransition(ctx, input, stepID, "delegate", signal.ActorID, signal.DelegateTo, "per_instance", idx, idx)
		return executeStep(ctx, input, idx, model.Step{
			ID: step.ID, Name: step.Name, Type: "approval",
			AssigneeID: signal.DelegateTo,
			SLAHours: step.SLAHours, TimeoutHours: step.TimeoutHours,
			EscalateToID: step.EscalateToID, OnExpire: step.OnExpire,
			Escalation: step.Escalation,
		})
	case "escalate":
		// ADR 0064 — manager-strategy escalation walks users.manager_id.
		// Falls back to the legacy escalate_to_id / Escalation.FixedTo
		// when manager_id isn't populated.
		nextAssignee := resolveEscalationTarget(ctx, input.TenantID, effective, step)
		if nextAssignee == "" {
			emitTransition(ctx, input, stepID, "expire", "", delegatorOnAssign, delegationKindOnAssign, idx, idx)
			return "rejected" // no escalation target → reject by ADR 0064 default
		}
		_ = workflow.ExecuteActivity(ctx, "NotifyAssignee", input.TenantID, nextAssignee, input.DocumentID, fmt.Sprintf("Escalated: %s", step.Name)).Get(ctx, nil)
		emitTransition(ctx, input, stepID, "escalate", "", nextAssignee, "", idx, idx)
		return executeStep(ctx, input, idx, model.Step{
			ID: step.ID, Name: step.Name + " (escalated)", Type: "approval",
			AssigneeID: nextAssignee,
			SLAHours: timeoutHours * 2, // bump the timer for the escalated assignee
			OnExpire: step.OnExpire, Escalation: decrementMax(step.Escalation),
		})
	case "recall":
		// ADR 0064 — initiator recalled BEFORE this approver acted.
		// The service-side gate (Service.RecallInstance) ensures we
		// only see this signal when no prior step has settled.
		_ = workflow.ExecuteActivity(ctx, "CompleteTask", input.TenantID, input.InstanceID, idx, "cancelled", "recalled by initiator").Get(ctx, nil)
		emitTransition(ctx, input, stepID, "recall", signal.ActorID, "", "", idx, idx)
		return "recalled"
	default:
		return "approved"
	}
}

// activitiesAssigneeResolution mirrors activities.AssigneeResolution.
// Duplicated here so the workflow package doesn't import activities
// (which would create a cycle).
type activitiesAssigneeResolution struct {
	EffectiveID    string `json:"effective_id"`
	DelegatorID    string `json:"delegator_id,omitempty"`
	DelegationKind string `json:"delegation_kind,omitempty"`
}

// emitTransition is a small wrapper around the EmitStepTransition
// activity that builds the dual-id audit payload.
func emitTransition(ctx workflow.Context, input model.ApprovalInput, stepID, outcome, actor, delegator, kind string, fromStep, toStep int) {
	_ = workflow.ExecuteActivity(ctx, "EmitStepTransition", input.TenantID, struct {
		InstanceID     string `json:"instance_id"`
		StepID         string `json:"step_id"`
		Outcome        string `json:"outcome"`
		ActorID        string `json:"actor_id"`
		DelegatorID    string `json:"delegator_id,omitempty"`
		DelegationKind string `json:"delegation_kind,omitempty"`
		FromStep       int    `json:"from_step"`
		ToStep         int    `json:"to_step"`
		At             string `json:"at"`
	}{
		InstanceID: input.InstanceID, StepID: stepID, Outcome: outcome,
		ActorID: actor, DelegatorID: delegator, DelegationKind: kind,
		FromStep: fromStep, ToStep: toStep,
	}).Get(ctx, nil)
}

// resolveEscalationTarget picks the next assignee per the step's
// escalation config. Manager strategy walks users.manager_id;
// fixed/chain fall back to legacy `escalate_to_id` when unset.
func resolveEscalationTarget(ctx workflow.Context, tenantID, currentAssignee string, step model.Step) string {
	if step.Escalation != nil {
		switch step.Escalation.Strategy {
		case "manager":
			levels := step.Escalation.MaxSteps
			if levels <= 0 {
				levels = 1
			}
			var next string
			if err := workflow.ExecuteActivity(ctx, "EscalateToManager", tenantID, currentAssignee, levels).Get(ctx, &next); err == nil && next != "" {
				return next
			}
			// Fall through to legacy fixed_to / escalate_to_id.
		case "fixed":
			if step.Escalation.FixedTo != "" {
				return step.Escalation.FixedTo
			}
		case "chain":
			if len(step.Escalation.Chain) > 0 {
				return step.Escalation.Chain[0]
			}
		}
	}
	return step.EscalateToID
}

// decrementMax returns a cloned escalation with MaxSteps reduced by
// one — used so the recursive escalate loop terminates after the
// configured number of hops.
func decrementMax(e *model.Escalation) *model.Escalation {
	if e == nil {
		return nil
	}
	out := *e
	if out.MaxSteps > 0 {
		out.MaxSteps--
	}
	if len(out.Chain) > 0 {
		out.Chain = out.Chain[1:]
	}
	return &out
}

// ParallelApprovalWorkflow runs multiple approvers concurrently.
func ParallelApprovalWorkflow(ctx workflow.Context, input model.ApprovalInput) (string, error) {
	if len(input.Steps) == 0 {
		return "approved", nil
	}
	step := input.Steps[0]
	mode := step.Mode
	if mode == "" {
		mode = "require_all"
	}

	approvers := step.Approvers
	if len(approvers) == 0 && step.AssigneeID != "" {
		approvers = []string{step.AssigneeID}
	}

	signalCh := workflow.GetSignalChannel(ctx, StepCompletedSignal)
	approved := 0
	rejected := 0
	needed := len(approvers)
	if mode == "require_any" {
		needed = 1
	}

	for range approvers {
		var signal model.StepSignal
		signalCh.Receive(ctx, &signal)
		if signal.Outcome == "approve" {
			approved++
		} else {
			rejected++
		}
		if mode == "require_any" && approved >= 1 {
			return "approved", nil
		}
		if mode == "require_all" && rejected > 0 {
			return "rejected", nil
		}
	}
	if approved >= needed {
		return "approved", nil
	}
	return "rejected", nil
}

// evaluateCondition dispatches between the legacy expr-lang activity
// and the ADR 0064 Rego activity based on which expression field is
// populated. Definitions saved via the new designer use ConditionRego;
// older definitions still use Condition.
func evaluateCondition(ctx workflow.Context, input model.ApprovalInput, step model.Step) (bool, error) {
	var result bool
	if step.ConditionRego != "" {
		err := workflow.ExecuteActivity(ctx, "EvaluateConditionRego", input.TenantID, input.DocumentID, step.ConditionRego).Get(ctx, &result)
		return result, err
	}
	err := workflow.ExecuteActivity(ctx, "EvaluateCondition", input.TenantID, input.DocumentID, step.Condition).Get(ctx, &result)
	return result, err
}
