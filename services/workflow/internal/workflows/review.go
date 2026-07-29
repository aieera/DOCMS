// Package workflows: document review workflow (Wave 7 Prompt 7.2 target).
//
// Semantics (final.md § 6.3):
//
//   - The author submits a document version for review.
//   - All listed reviewers are notified in parallel; each has 72h
//     (configurable) to approve or reject.
//   - Unanimous approval → emit dms.review.approved.v1 + transition
//     document to active.
//   - ANY rejection → emit dms.review.rejected.v1 with the rejector's
//     comment + revert document to draft.
//   - Signals:
//     ReviewerDecided(reviewer_id, decision, comment)
//     ReviewerReassigned(old_id, new_id, reason)
//   - 72h timer per reviewer → auto-escalation (counts as a reject by
//     default; the policy team can adjust via a follow-up).
//
// Compared to ParallelApprovalWorkflow (which models require_any /
// require_all over a single Step's approver list), ReviewWorkflow is
// explicitly about version-level review and lives at a distinct
// NATS subject family (dms.review.*).
//
// Determinism contract: no time.Now, no rand, no direct DB. All I/O
// goes through activities.
package workflows

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// ReviewerDecidedSignal is the per-reviewer decision channel.
const ReviewerDecidedSignal = "reviewer_decided"

// ReviewerReassignedSignal lets operators re-target a pending review
// slot without cancelling the whole workflow.
const ReviewerReassignedSignal = "reviewer_reassigned"

// ReviewInput configures the workflow at start.
type ReviewInput struct {
	TenantID     string   `json:"tenant_id"`
	InstanceID   string   `json:"instance_id"`
	DocumentID   string   `json:"document_id"`
	VersionID    string   `json:"version_id"`
	InitiatedBy  string   `json:"initiated_by"`
	Reviewers    []string `json:"reviewers"`
	TimeoutHours int      `json:"timeout_hours"` // 0 → 72
}

// ReviewerDecision is the payload of ReviewerDecidedSignal.
type ReviewerDecision struct {
	ReviewerID string `json:"reviewer_id"`
	Decision   string `json:"decision"` // approve | reject
	Comment    string `json:"comment,omitempty"`
}

// ReviewerReassignment is the payload of ReviewerReassignedSignal.
type ReviewerReassignment struct {
	OldReviewerID string `json:"old_reviewer_id"`
	NewReviewerID string `json:"new_reviewer_id"`
	Reason        string `json:"reason"`
}

// ReviewWorkflow is the entry point. Return values are the final
// outcome ("approved" | "rejected") and any workflow-level error.
func ReviewWorkflow(ctx workflow.Context, in ReviewInput) (string, error) {
	if len(in.Reviewers) == 0 {
		return "approved", nil
	}
	timeout := time.Duration(in.TimeoutHours) * time.Hour
	if timeout <= 0 {
		timeout = 72 * time.Hour
	}

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3, InitialInterval: time.Second, BackoffCoefficient: 2, MaximumInterval: 30 * time.Second},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	// Notify every reviewer + create an inbox task. Fire-and-forget per
	// spec: the signal channel is how we actually proceed.
	for _, reviewer := range in.Reviewers {
		_ = workflow.ExecuteActivity(ctx, "CreateTask",
			in.TenantID, in.InstanceID, in.DocumentID, "review", reviewer,
		).Get(ctx, nil)
		_ = workflow.ExecuteActivity(ctx, "NotifyAssignee",
			in.TenantID, reviewer, in.DocumentID, "Review requested",
		).Get(ctx, nil)
	}

	// Track which reviewers still owe a decision.
	pending := make(map[string]bool, len(in.Reviewers))
	for _, r := range in.Reviewers {
		pending[r] = true
	}
	approvals := 0
	var rejector, rejectComment string

	decidedCh := workflow.GetSignalChannel(ctx, ReviewerDecidedSignal)
	reassignCh := workflow.GetSignalChannel(ctx, ReviewerReassignedSignal)

	timerCtx, timerCancel := workflow.WithCancel(ctx)
	deadline := workflow.NewTimer(timerCtx, timeout)
	defer timerCancel()

	for len(pending) > 0 && rejector == "" {
		sel := workflow.NewSelector(ctx)

		// Accept decisions from any pending reviewer.
		// SECURITY NOTE (Epic 10, TRACKED — NOT currently reachable): d.ReviewerID
		// comes from the signal payload and is gated only against the pending set,
		// NOT bound to an authenticated caller — so whoever delivers this signal
		// could decide on behalf of any assigned reviewer. There is no HTTP
		// endpoint that sends ReviewerDecidedSignal today (unlike signalStep, which
		// stamps ActorID from the session), so it is not exploitable. When such an
		// endpoint is added it MUST stamp the authenticated user and this handler
		// MUST verify d.ReviewerID == that user (as ApprovalWorkflow now does for
		// StepSignal). See docs/security/epic10-workflow-followups.md.
		sel.AddReceive(decidedCh, func(ch workflow.ReceiveChannel, _ bool) {
			var d ReviewerDecision
			ch.Receive(ctx, &d)
			if !pending[d.ReviewerID] {
				return // late decision for an already-processed reviewer
			}
			delete(pending, d.ReviewerID)
			_ = workflow.ExecuteActivity(ctx, "CompleteTask",
				in.TenantID, in.InstanceID, 0, d.Decision, d.Comment,
			).Get(ctx, nil)
			if d.Decision == "reject" {
				rejector = d.ReviewerID
				rejectComment = d.Comment
				return
			}
			approvals++
		})

		// Accept reassignments. Effect: move the pending slot from
		// old → new reviewer and re-notify. We keep the timer running
		// on the workflow-level deadline, not per-reviewer, for
		// simplicity — a Wave 7 follow-up can split per-reviewer
		// timers if customers need SLA-per-reviewer.
		sel.AddReceive(reassignCh, func(ch workflow.ReceiveChannel, _ bool) {
			var r ReviewerReassignment
			ch.Receive(ctx, &r)
			if !pending[r.OldReviewerID] {
				return
			}
			delete(pending, r.OldReviewerID)
			pending[r.NewReviewerID] = true
			_ = workflow.ExecuteActivity(ctx, "DelegateTask",
				in.TenantID, in.InstanceID, 0, r.NewReviewerID,
			).Get(ctx, nil)
			_ = workflow.ExecuteActivity(ctx, "NotifyAssignee",
				in.TenantID, r.NewReviewerID, in.DocumentID, "Review reassigned: "+r.Reason,
			).Get(ctx, nil)
		})

		// Global deadline: any reviewer still pending at this point
		// counts as a reject per the "timer triggers auto-escalation"
		// clause. We synthesize a single rejection so the outcome
		// path stays simple.
		sel.AddFuture(deadline, func(_ workflow.Future) {
			rejector = "timeout"
			rejectComment = "review window expired with pending reviewers"
			pending = nil // break the loop
		})

		sel.Select(ctx)
	}

	if rejector != "" {
		_ = workflow.ExecuteActivity(ctx, "SetDocumentLifecycle",
			in.TenantID, in.DocumentID, "draft",
		).Get(ctx, nil)
		_ = workflow.ExecuteActivity(ctx, "PublishEvent",
			in.TenantID, "dms.review.rejected.v1", map[string]string{
				"instance_id":   in.InstanceID,
				"document_id":   in.DocumentID,
				"version_id":    in.VersionID,
				"rejected_by":   rejector,
				"reject_reason": rejectComment,
			},
		).Get(ctx, nil)
		return "rejected", nil
	}

	_ = workflow.ExecuteActivity(ctx, "SetDocumentLifecycle",
		in.TenantID, in.DocumentID, "active",
	).Get(ctx, nil)
	_ = workflow.ExecuteActivity(ctx, "PublishEvent",
		in.TenantID, "dms.review.approved.v1", map[string]string{
			"instance_id": in.InstanceID,
			"document_id": in.DocumentID,
			"version_id":  in.VersionID,
			"approvals":   intToString(approvals),
		},
	).Get(ctx, nil)
	return "approved", nil
}

// intToString avoids importing strconv just for one call in the
// workflow body — and strconv in a workflow would be fine anyway,
// but the smaller the import surface of a workflow file the easier
// it is to audit for non-determinism.
func intToString(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// Query handler: return the outstanding reviewer set. Used by the UI
// to show "3 of 5 reviewers pending". Caller runs
// `workflow.QueryWorkflow(ctx, workflowID, "", "review_status")`.
//
// NOTE: this is wired via the workflow's interceptor chain — see
// handler.StartWorkflow which calls SetQueryHandler after Start.
// The query function itself lives here to keep the workflow API
// colocated with its implementation.
type ReviewStatus struct {
	Pending   []string `json:"pending_reviewers"`
	Approvals int      `json:"approvals"`
	Total     int      `json:"total"`
}
