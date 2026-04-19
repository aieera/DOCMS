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
		if step.Type == "condition" {
			condResult, err := evaluateCondition(ctx, input, step)
			if err != nil {
				return "error", err
			}
			if condResult {
				for _, s := range step.OnTrue {
					if outcome := executeStep(ctx, input, i, s); outcome == "rejected" {
						return "rejected", nil
					}
				}
			} else {
				for _, s := range step.OnFalse {
					if outcome := executeStep(ctx, input, i, s); outcome == "rejected" {
						return "rejected", nil
					}
				}
			}
			continue
		}

		outcome := executeStep(ctx, input, i, step)
		if outcome == "rejected" {
			_ = workflow.ExecuteActivity(ctx, "SetDocumentLifecycle", input.TenantID, input.DocumentID, "draft").Get(ctx, nil)
			return "rejected", nil
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

	_ = workflow.ExecuteActivity(ctx, "CreateTask", input.TenantID, input.InstanceID, input.DocumentID, step.Name, step.AssigneeID).Get(ctx, nil)
	_ = workflow.ExecuteActivity(ctx, "NotifyAssignee", input.TenantID, step.AssigneeID, input.DocumentID, step.Name).Get(ctx, nil)

	timeout := time.Duration(step.TimeoutHours) * time.Hour
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
		signal = model.StepSignal{StepIndex: idx, Outcome: "escalate"}
	})
	sel.Select(ctx)

	switch signal.Outcome {
	case "approve":
		_ = workflow.ExecuteActivity(ctx, "CompleteTask", input.TenantID, input.InstanceID, idx, "approved", signal.Notes).Get(ctx, nil)
		return "approved"
	case "reject":
		_ = workflow.ExecuteActivity(ctx, "CompleteTask", input.TenantID, input.InstanceID, idx, "rejected", signal.Notes).Get(ctx, nil)
		return "rejected"
	case "delegate":
		_ = workflow.ExecuteActivity(ctx, "DelegateTask", input.TenantID, input.InstanceID, idx, signal.DelegateTo).Get(ctx, nil)
		return executeStep(ctx, input, idx, model.Step{
			Name: step.Name, Type: "approval", AssigneeID: signal.DelegateTo,
			TimeoutHours: step.TimeoutHours, EscalateToID: step.EscalateToID,
		})
	case "escalate":
		if step.EscalateToID != "" {
			_ = workflow.ExecuteActivity(ctx, "NotifyAssignee", input.TenantID, step.EscalateToID, input.DocumentID, fmt.Sprintf("Escalated: %s", step.Name)).Get(ctx, nil)
		}
		return executeStep(ctx, input, idx, model.Step{
			Name: step.Name + " (escalated)", Type: "approval",
			AssigneeID: step.EscalateToID, TimeoutHours: step.TimeoutHours * 2,
		})
	default:
		return "approved"
	}
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

func evaluateCondition(ctx workflow.Context, input model.ApprovalInput, step model.Step) (bool, error) {
	var result bool
	err := workflow.ExecuteActivity(ctx, "EvaluateCondition", input.TenantID, input.DocumentID, step.Condition).Get(ctx, &result)
	return result, err
}
