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

// lifecycleHeldChangeID versions the lifecycle_held command (review M-2).
// See applyDocumentLifecycle.
const lifecycleHeldChangeID = "def02-lifecycle-held"

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
// The lifecycle_held PublishEvent is a command the pre-DEF-02 code never
// issued (it discarded the activity's error), so it is gated with
// workflow.GetVersion(lifecycleHeldChangeID) (review M-2): a history
// recorded before this change has no version marker and replays with
// DefaultVersion on the old path — no extra command — instead of failing
// as nondeterministic (reject: the old history completes the workflow
// there) or matching the held event against the old "completed"
// PublishEvent (approve). GetVersion cannot make an OLD worker replay a
// NEW history; a rolling deploy still has to drain old workflow workers.
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
			if workflow.GetVersion(ctx, lifecycleHeldChangeID, workflow.DefaultVersion, 1) == workflow.DefaultVersion {
				return // pre-change history: no held event
			}
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
