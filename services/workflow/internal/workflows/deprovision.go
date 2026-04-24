// Package workflows — tenant de-provisioning with 30-day soft-delete
// window and crypto-shred finish.
//
// Shape:
//   1. DeprovisionWorkflow runs per tenant at soft-delete time.
//   2. Sleeps for `graceDays` (default 30); client can signal
//      "undo" to exit early.
//   3. On expiry, runs HardDisposeActivity which is a placeholder for
//      the cross-service purge + KMS CMK shred. Marks disposed_at
//      and emits `dms.tenant.disposed.v1`.
//
// The CMK shred itself is a KMS-adapter concern: AWS KMS schedules
// CMK deletion with a 7-day minimum window; Vault Transit has no
// delete-with-grace so the shred is an immediate purge of the alias
// from tenant_keks + crypto-erase of the per-tenant master. The
// activity today logs the intent and marks the tenant disposed so
// the workflow completes; the real shred lands in a follow-up
// tracked on the ledger.

package workflows

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	UndoDeprovisionSignal = "undo_deprovision"
	DefaultGraceDays      = 30
)

type DeprovisionInput struct {
	TenantID  string `json:"tenant_id"`
	GraceDays int    `json:"grace_days"` // 0 → DefaultGraceDays
}

type DeprovisionOutcome struct {
	Status    string `json:"status"` // disposed | cancelled
	DisposeID string `json:"dispose_id,omitempty"`
}

// DeprovisionWorkflow is the 30-day lifecycle timer. Billing's
// `/deprovision` endpoint starts this workflow with WorkflowID
// `deprovision-<tenant_id>` so Temporal refuses to start a second
// one for the same tenant (idempotent at the orchestration layer).
func DeprovisionWorkflow(ctx workflow.Context, in DeprovisionInput) (*DeprovisionOutcome, error) {
	logger := workflow.GetLogger(ctx)
	grace := in.GraceDays
	if grace <= 0 {
		grace = DefaultGraceDays
	}
	logger.Info("deprovision started", "tenant", in.TenantID, "grace_days", grace)

	// Timer + signal race. Undo cancels the workflow cleanly before
	// HardDispose runs.
	timerCtx, cancelTimer := workflow.WithCancel(ctx)
	timer := workflow.NewTimer(timerCtx, time.Duration(grace)*24*time.Hour)
	sig := workflow.GetSignalChannel(ctx, UndoDeprovisionSignal)

	var undone bool
	sel := workflow.NewSelector(ctx)
	sel.AddFuture(timer, func(_ workflow.Future) {})
	sel.AddReceive(sig, func(ch workflow.ReceiveChannel, _ bool) {
		var v any
		ch.Receive(ctx, &v)
		undone = true
		cancelTimer()
	})
	sel.Select(ctx)

	if undone {
		logger.Info("deprovision cancelled by undo signal", "tenant", in.TenantID)
		return &DeprovisionOutcome{Status: "cancelled"}, nil
	}

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts:    5,
			InitialInterval:    10 * time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    5 * time.Minute,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	var disposeID string
	if err := workflow.ExecuteActivity(ctx, "HardDisposeTenant", in.TenantID).Get(ctx, &disposeID); err != nil {
		return nil, err
	}
	_ = workflow.ExecuteActivity(ctx, "PublishEvent", in.TenantID, "dms.tenant.disposed.v1", map[string]string{
		"tenant_id":   in.TenantID,
		"dispose_id":  disposeID,
	}).Get(ctx, nil)

	return &DeprovisionOutcome{Status: "disposed", DisposeID: disposeID}, nil
}
