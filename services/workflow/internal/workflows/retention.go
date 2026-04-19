// Package workflows — retention cron (Wave 8.1 real body).
//
// Semantics (final.md § 7.1):
//
//   - Runs as a Temporal schedule (daily 03:00 UTC per tenant, set up by
//     cmd/worker on boot via RegisterRetentionSchedules).
//   - Sweeps documents whose retention clock has expired.
//   - Skips documents with an active legal hold (emits
//     `dms.retention.held.v1`).
//   - Transitions `active|retained` → `archived` and emits
//     `dms.retention.archived.v1`.
//   - Flags documents that have been archived ≥ `ArchiveDays` as
//     dispose candidates by emitting `dms.retention.dispose_candidate.v1`.
//     Actual disposition is interactive (two-person approval) and
//     runs in a separate workflow — logged in out-of-scope.
//   - DryRun=true returns counts only, no state change, no event.
//
// Determinism: no time.Now, no rand, no direct DB. All I/O goes
// through activities registered on the worker.
package workflows

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/vaultdms/vaultdms/services/workflow/internal/activities"
)

// Domain-event subjects used by the retention cron. Kept as constants
// so callers searching the codebase find all emission points.
const (
	SubjectRetentionArchived         = "dms.retention.archived.v1"
	SubjectRetentionDisposeCandidate = "dms.retention.dispose_candidate.v1"
	SubjectRetentionHeld             = "dms.retention.held.v1"
)

// DefaultArchiveDays is the grace period between `archived` and
// `dispose-candidate` emission. Spec § 7.1 puts it at 30 days.
const DefaultArchiveDays = 30

// RetentionInput drives one cron invocation.
type RetentionInput struct {
	TenantID string `json:"tenant_id"`
	// DryRun = true lists what would happen but takes no action.
	DryRun bool `json:"dry_run"`
	// ArchiveDays overrides DefaultArchiveDays (0 uses default).
	ArchiveDays int `json:"archive_days,omitempty"`
	// BatchLimit caps the per-run doc count (0 → activity default).
	BatchLimit int `json:"batch_limit,omitempty"`
}

// RetentionOutcome summarises what the cron run did.
type RetentionOutcome struct {
	TenantID          string   `json:"tenant_id"`
	Archived          int      `json:"archived"`
	DisposeCandidates int      `json:"dispose_candidates"`
	Disposed          int      `json:"disposed"`
	HeldSkipped       int      `json:"held_skipped"`
	Errors            []string `json:"errors,omitempty"`
	ExecutedAt        string   `json:"executed_at"`
}

// RetentionWorkflow is the cron entrypoint.
func RetentionWorkflow(ctx workflow.Context, in RetentionInput) (*RetentionOutcome, error) {
	logger := workflow.GetLogger(ctx)
	archiveDays := in.ArchiveDays
	if archiveDays <= 0 {
		archiveDays = DefaultArchiveDays
	}

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts:    3,
			InitialInterval:    10 * time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    2 * time.Minute,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	asOf := workflow.Now(ctx).UTC()
	out := &RetentionOutcome{
		TenantID:   in.TenantID,
		ExecutedAt: asOf.Format(time.RFC3339),
	}

	// Sweep.
	var expired []activities.ExpiredDocument
	if err := workflow.ExecuteActivity(ctx, "SweepExpiredRetentions",
		in.TenantID, asOf, in.BatchLimit,
	).Get(ctx, &expired); err != nil {
		return out, err
	}
	logger.Info("retention sweep", "tenant_id", in.TenantID, "found", len(expired), "dry_run", in.DryRun)

	archiveCutoff := asOf.Add(-time.Duration(archiveDays) * 24 * time.Hour)

	for _, doc := range expired {
		// Legal-hold gate. A held document is skipped regardless of its
		// current lifecycle; holds always win.
		var held bool
		if err := workflow.ExecuteActivity(ctx, "DocumentOnLegalHold",
			in.TenantID, doc.DocumentID,
		).Get(ctx, &held); err != nil {
			out.Errors = append(out.Errors, "hold_check:"+doc.DocumentID+":"+err.Error())
			continue
		}
		if held {
			out.HeldSkipped++
			if !in.DryRun {
				_ = workflow.ExecuteActivity(ctx, "EmitRetentionEvent",
					in.TenantID, doc.DocumentID, SubjectRetentionHeld, "retention expired during active legal hold",
				).Get(ctx, nil)
			}
			continue
		}

		switch doc.LifecycleState {
		case "active", "retained":
			if in.DryRun {
				out.Archived++
				continue
			}
			if err := workflow.ExecuteActivity(ctx, "RetentionTransition",
				in.TenantID, doc.DocumentID, "archived", SubjectRetentionArchived,
			).Get(ctx, nil); err != nil {
				out.Errors = append(out.Errors, "archive:"+doc.DocumentID+":"+err.Error())
				continue
			}
			out.Archived++

		case "archived":
			// Flag as dispose candidate if it has been archived long
			// enough. We use updated_at as the last-transition marker;
			// the cron only touches updated_at when it transitions.
			if doc.UpdatedAt.After(archiveCutoff) {
				continue
			}
			out.DisposeCandidates++
			if !in.DryRun {
				_ = workflow.ExecuteActivity(ctx, "EmitRetentionEvent",
					in.TenantID, doc.DocumentID, SubjectRetentionDisposeCandidate, "archived ≥ grace period",
				).Get(ctx, nil)
			}
		}
	}

	return out, nil
}
