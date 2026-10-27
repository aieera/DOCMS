package workflows

import (
	"errors"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/aieera/sedoc/services/workflow/internal/activities"
)

// SubjectWorkflowLifecycleHeld records that a workflow's decision could
// not be applied to its document because the document is under legal
// hold. The workflow-domain counterpart of the retention cron's
// dms.retention.held.v1: in the WORKFLOW_EVENTS stream (dms.workflow.>)
// and therefore in the audit hash chain, which subscribes to
// dms.workflow.>.
const SubjectWorkflowLifecycleHeld = "dms.workflow.lifecycle_held.v1"

// applyDocumentLifecycle runs SetDocumentLifecycle for a decided
// approval/review and handles its refusals so the workflow still
// completes with the reviewers' decision:
//
//   - LegalHold (non-retryable): the document is left unchanged and the
//     hold is recorded with a lifecycle_held event naming the document and
//     the state it was not moved to — as the retention cron records a held
//     document;
//   - InvalidLifecycleTransition / DocumentNotFound (non-retryable): the
//     document is left unchanged and the workflow logs it;
//   - anything else (retries exhausted): logged, as before.
//
// Called only after the decision is made, so the extra event is a new
// command after every existing history point — replay-safe for in-flight
// executions.
func applyDocumentLifecycle(ctx workflow.Context, tenantID, instanceID, documentID, target string) {
	err := workflow.ExecuteActivity(ctx, "SetDocumentLifecycle", tenantID, documentID, target).Get(ctx, nil)
	if err == nil {
		return
	}
	logger := workflow.GetLogger(ctx)
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) {
		switch appErr.Type() {
		case activities.ErrTypeLegalHold:
			logger.Warn("document under legal hold; lifecycle unchanged",
				"document", documentID, "target_state", target)
			_ = workflow.ExecuteActivity(ctx, "PublishEvent", tenantID, SubjectWorkflowLifecycleHeld, map[string]string{
				"instance_id":  instanceID,
				"document_id":  documentID,
				"target_state": target,
				"reason":       "workflow outcome not applied: document is under an active legal hold",
			}).Get(ctx, nil)
			return
		case activities.ErrTypeInvalidLifecycleTransition, activities.ErrTypeDocumentNotFound:
			logger.Warn("workflow outcome not applied to document",
				"document", documentID, "target_state", target, "reason", appErr.Error())
			return
		}
	}
	logger.Error("set document lifecycle failed", "document", documentID, "target_state", target, "error", err)
}
