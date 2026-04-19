// Package workflows — residency migration (Wave 8.4).
//
// Resumable pattern:
//
//   1. Enumerate — INSERT ... ON CONFLICT DO NOTHING into
//      residency_migration_items, so a resume is idempotent.
//   2. Loop — NextPendingMigrationDoc returns up to BatchSize ids,
//      workflow calls MoveDocumentRegion on each. Both activities
//      are idempotent.
//   3. Finalize — FinalizeMigration computes terminal status from
//      the item rows.
//
// If the worker crashes between steps 2 and 3, Temporal replay
// restarts at the top of the loop; NextPendingMigrationDoc returns
// only still-pending items so no work is duplicated.
package workflows

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// SubjectResidencyMigrated is the per-doc event emitted inside
// MoveDocumentRegion. Exported here so integration tests can subscribe.
const SubjectResidencyMigrated = "dms.residency.migrated.v1"

// ResidencyMigrationInput configures one migration run.
type ResidencyMigrationInput struct {
	TenantID            string `json:"tenant_id"`
	MigrationID         string `json:"migration_id"`
	SourceRegion        string `json:"source_region"`
	TargetRegion        string `json:"target_region"`
	FilterWorkspace     string `json:"filter_workspace,omitempty"`
	FilterDocumentClass string `json:"filter_document_class,omitempty"`
	BatchSize           int    `json:"batch_size,omitempty"`
}

// ResidencyMigrationOutcome summarises the run.
type ResidencyMigrationOutcome struct {
	MigrationID string `json:"migration_id"`
	Status      string `json:"status"` // completed | failed | running
	Moved       int    `json:"moved"`
	Failed      int    `json:"failed"`
}

// ResidencyMigrationWorkflow is the entrypoint. Designed to be
// replayed safely: every activity is idempotent; no decisions depend
// on wall-clock time except workflow.Now, which Temporal replays
// deterministically.
func ResidencyMigrationWorkflow(ctx workflow.Context, in ResidencyMigrationInput) (*ResidencyMigrationOutcome, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts:    5,
			InitialInterval:    5 * time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    2 * time.Minute,
		},
	})
	logger := workflow.GetLogger(ctx)

	batch := in.BatchSize
	if batch <= 0 {
		batch = 50
	}
	out := &ResidencyMigrationOutcome{MigrationID: in.MigrationID}

	// Step 1 — enumerate (idempotent).
	var enqueued int
	if err := workflow.ExecuteActivity(ctx, "EnumerateDocsForMigration",
		in.TenantID, in.MigrationID, in.SourceRegion, in.FilterWorkspace, in.FilterDocumentClass,
	).Get(ctx, &enqueued); err != nil {
		return out, err
	}
	logger.Info("residency migration enumerated", "count", enqueued, "migration_id", in.MigrationID)

	// Step 2 — loop until no pending items remain.
	for {
		var pending []string
		if err := workflow.ExecuteActivity(ctx, "NextPendingMigrationDoc",
			in.TenantID, in.MigrationID, batch,
		).Get(ctx, &pending); err != nil {
			return out, err
		}
		if len(pending) == 0 {
			break
		}
		for _, docID := range pending {
			if err := workflow.ExecuteActivity(ctx, "MoveDocumentRegion",
				in.TenantID, in.MigrationID, docID, in.TargetRegion,
			).Get(ctx, nil); err != nil {
				// Record the per-doc failure and continue; a whole-
				// migration failure would block operators from finishing
				// the successful moves.
				_ = workflow.ExecuteActivity(ctx, "MarkItemFailed",
					in.TenantID, in.MigrationID, docID, err.Error(),
				).Get(ctx, nil)
				out.Failed++
				continue
			}
			out.Moved++
		}
	}

	// Step 3 — finalize (idempotent).
	var status string
	if err := workflow.ExecuteActivity(ctx, "FinalizeMigration",
		in.TenantID, in.MigrationID,
	).Get(ctx, &status); err != nil {
		return out, err
	}
	out.Status = status
	return out, nil
}
