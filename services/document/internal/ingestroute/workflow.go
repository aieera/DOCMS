// Package ingestroute holds the WS3 IngestAndRoute Temporal workflow and its
// activities. The workflow runs on its own task queue (TaskQueue) served by an
// embedded worker in the document service, so the activities call the document
// service's service layer in-process — no cross-service gRPC for the staging,
// matching, and commit steps.
//
// Trigger: the document service's processed-event consumer starts this workflow
// (workflow-id ingest-<item_id>, RejectDuplicate) when the intelligence worker
// emits dms.ingestion.processed.v1. The flow mirrors the spec's activity chain
// extractKey → matchExisting → decide → commit; the decision lives in the
// Decide activity (not the workflow body) so the confidence-threshold read
// stays out of the deterministic workflow path.
package ingestroute

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// TaskQueue is the dedicated Temporal task queue the document-service worker
// serves IngestAndRoute on. Kept distinct from the workflow service's
// vaultdms-default queue so the two workers don't compete for each other's tasks.
const TaskQueue = "vaultdms-ingest"

// IngestInput starts the workflow. IDs are strings (Temporal payloads are JSON).
type IngestInput struct {
	TenantID string `json:"tenant_id"`
	ItemID   string `json:"item_id"`
}

// IngestResult is the workflow outcome.
type IngestResult struct {
	Outcome    string `json:"outcome"` // committed | needs_review | noop
	DocumentID string `json:"document_id,omitempty"`
	VersionID  string `json:"version_id,omitempty"`
	ReviewID   string `json:"review_id,omitempty"`
}

// IngestAndRoute decides what to do with a processed staging item: commit it as
// a new version / new document (Workstream-1 upsert) when the read is
// high-confidence, or park it in the review queue otherwise. Idempotent: the
// activities no-op on an already-terminal item, and the upsert is a same-bytes
// no-op on (tenant, external_key, checksum).
func IngestAndRoute(ctx workflow.Context, in IngestInput) (IngestResult, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 60 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts:    5,
			InitialInterval:    time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    time.Minute,
		},
	})

	// extractKey — read the worker's OCR/extraction result off the staging row.
	var ek ExtractKeyResult
	if err := workflow.ExecuteActivity(ctx, "ExtractKey", in.TenantID, in.ItemID).Get(ctx, &ek); err != nil {
		return IngestResult{}, err
	}
	if ek.Terminal {
		// Already routed by a prior run (at-least-once redelivery).
		return IngestResult{Outcome: "noop"}, nil
	}

	// matchExisting — find a live document carrying the extracted business key.
	var matchDocID string
	if err := workflow.ExecuteActivity(ctx, "MatchExisting", in.TenantID, in.ItemID).Get(ctx, &matchDocID); err != nil {
		return IngestResult{}, err
	}

	// decide — gate on the confidence threshold (read inside the activity).
	var dec DecideResult
	if err := workflow.ExecuteActivity(ctx, "Decide", ek.ExternalKey, ek.Confidence, matchDocID).Get(ctx, &dec); err != nil {
		return IngestResult{}, err
	}

	// commit — high confidence → version/new-doc via the upsert path.
	if dec.Action == "commit" {
		var c CommitResult
		if err := workflow.ExecuteActivity(ctx, "Commit", in.TenantID, in.ItemID).Get(ctx, &c); err != nil {
			return IngestResult{}, err
		}
		return IngestResult{Outcome: "committed", DocumentID: c.DocumentID, VersionID: c.VersionID}, nil
	}

	// otherwise → review queue, no version written.
	var reviewID string
	if err := workflow.ExecuteActivity(ctx, "NeedsReview", in.TenantID, in.ItemID, matchDocID, dec.Reason).Get(ctx, &reviewID); err != nil {
		return IngestResult{}, err
	}
	return IngestResult{Outcome: "needs_review", ReviewID: reviewID}, nil
}
