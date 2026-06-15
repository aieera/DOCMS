//go:build integration
// +build integration

// Acceptance tests for Workstream 4 (native review/triage queue). These pin the
// spec's guarantees on top of the WS3 routing path:
//
//  1. a low-confidence ingestion appears as a PENDING review item and emits
//     dms.review.created.v1.
//  2. resolving "new_version" creates the version on the chosen document and
//     clears the staged item (committed) + emits dms.review.resolved.v1.
//  3. resolving "new_document" files the read standalone (new doc + v1).
//  4. resolving "reject" discards WITHOUT writing a version.
//  5. an already-resolved item cannot be resolved again (Conflict).
//
// Run with: go test -tags integration ./services/document/internal/service/...
//
// Reuses externalKeyFixture + seed/count helpers + routeOutcome/markIngestionProcessed.
package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// stageForReview creates a staged item with the given external key at LOW
// confidence and routes it to the review queue. Returns the ingestion item id +
// the resulting review item id.
func stageForReview(t *testing.T, callCtx context.Context, svc *service.DocumentService, pool *pgxpool.Pool, tenant, wsID, folderID uuid.UUID, sha, key string) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	seedBlobSHA(ctx, t, pool, tenant, uuid.Must(uuid.NewV7()), sha, 1024, "application/pdf")
	item, err := svc.CreateIngestionItem(callCtx, &service.CreateIngestionInput{
		WorkspaceID: wsID, FolderID: folderID, TargetCustomerRef: "cust",
		BlobChecksum: sha, DocumentClass: "invoice",
	})
	require.NoError(t, err)
	markIngestionProcessed(ctx, t, pool, tenant, item.ID, key, 0.40) // below 0.85
	outcome, _, reviewID := routeOutcome(t, svc, tenant, item.ID)
	require.Equal(t, "needs_review", outcome)
	require.NotEmpty(t, reviewID)
	return item.ID, uuid.MustParse(reviewID)
}

func TestReviewQueue_LowConfidence_PendingAndEvent(t *testing.T) {
	callCtx, svc, pool, tenant, wsID, folderID := externalKeyFixture(t)
	ctx := context.Background()

	const sha = "6161616161616161616161616161616161616161616161616161616161616161"
	itemID, reviewID := stageForReview(t, callCtx, svc, pool, tenant, wsID, folderID, sha, "INV-REV-1")

	// Pending + keyset-listable.
	items, err := svc.ListReviewQueueKeyset(callCtx, string(model.ReviewPending), time.Time{}, uuid.Nil, 50)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, reviewID, items[0].ID)
	require.Equal(t, itemID, items[0].IngestionItemID)
	require.Equal(t, string(model.ReviewPending), string(items[0].Status))

	// dms.review.created.v1 landed on the outbox.
	require.Equal(t, 1, countOutbox(ctx, t, pool, tenant, "dms.review.created.v1"))
}

func TestReviewQueue_ResolveNewVersion_VersionsAndClears(t *testing.T) {
	callCtx, svc, pool, tenant, wsID, folderID := externalKeyFixture(t)
	ctx := context.Background()
	user := callerUser(t, callCtx)

	// A target document to receive the new version.
	const shaTarget = "7171717171717171717171717171717171717171717171717171717171717171"
	seedBlobSHA(ctx, t, pool, tenant, uuid.Must(uuid.NewV7()), shaTarget, 1024, "application/pdf")
	target, err := svc.UpsertDocumentByExternalKey(callCtx, &service.UpsertByExternalKeyInput{
		ExternalID: "ACME-001", WorkspaceID: wsID, FolderID: folderID,
		BlobChecksum: shaTarget, UpdatedBy: user,
	})
	require.NoError(t, err)

	const sha = "7272727272727272727272727272727272727272727272727272727272727272"
	itemID, reviewID := stageForReview(t, callCtx, svc, pool, tenant, wsID, folderID, sha, "ACME-AMBIG")

	res, err := svc.ResolveReviewItem(callCtx, &service.ResolveReviewInput{
		ReviewItemID: reviewID, Decision: "new_version", TargetDocumentID: target.DocumentID,
	})
	require.NoError(t, err)
	require.Equal(t, model.ReviewResolved, res.Status)
	require.Equal(t, target.DocumentID, res.DocumentID)

	require.Equal(t, 2, countVersions(ctx, t, pool, tenant, target.DocumentID), "new version appended to the chosen doc")
	require.Equal(t, string(model.IngestCommitted), ingestionStatus(ctx, t, pool, tenant, itemID), "staged item cleared")
	require.Equal(t, string(model.ReviewResolved), reviewStatus(ctx, t, pool, tenant, reviewID))
	require.Equal(t, 1, countOutbox(ctx, t, pool, tenant, "dms.review.resolved.v1"))

	// Already-resolved → Conflict.
	_, err = svc.ResolveReviewItem(callCtx, &service.ResolveReviewInput{
		ReviewItemID: reviewID, Decision: "new_version", TargetDocumentID: target.DocumentID,
	})
	require.Error(t, err)
}

func TestReviewQueue_ResolveNewDocument_FilesStandalone(t *testing.T) {
	callCtx, svc, pool, tenant, wsID, folderID := externalKeyFixture(t)
	ctx := context.Background()

	const sha = "8181818181818181818181818181818181818181818181818181818181818181"
	itemID, reviewID := stageForReview(t, callCtx, svc, pool, tenant, wsID, folderID, sha, "STANDALONE-1")

	res, err := svc.ResolveReviewItem(callCtx, &service.ResolveReviewInput{
		ReviewItemID: reviewID, Decision: "new_document",
	})
	require.NoError(t, err)
	require.Equal(t, model.ReviewResolved, res.Status)
	require.NotEqual(t, uuid.Nil, res.DocumentID)

	require.Equal(t, 1, countDocs(ctx, t, pool, tenant, "STANDALONE-1"), "filed as a standalone document")
	require.Equal(t, 1, countVersions(ctx, t, pool, tenant, res.DocumentID))
	require.Equal(t, string(model.IngestCommitted), ingestionStatus(ctx, t, pool, tenant, itemID))
}

func TestReviewQueue_Reject_DiscardsNoVersion(t *testing.T) {
	callCtx, svc, pool, tenant, wsID, folderID := externalKeyFixture(t)
	ctx := context.Background()

	const sha = "9191919191919191919191919191919191919191919191919191919191919191"
	itemID, reviewID := stageForReview(t, callCtx, svc, pool, tenant, wsID, folderID, sha, "REJECT-1")

	res, err := svc.ResolveReviewItem(callCtx, &service.ResolveReviewInput{
		ReviewItemID: reviewID, Decision: "reject", Notes: "not a real invoice",
	})
	require.NoError(t, err)
	require.Equal(t, model.ReviewRejected, res.Status)

	require.Equal(t, 0, countDocs(ctx, t, pool, tenant, "REJECT-1"), "reject must not create a document")
	require.Equal(t, string(model.IngestRejected), ingestionStatus(ctx, t, pool, tenant, itemID))
	require.Equal(t, string(model.ReviewRejected), reviewStatus(ctx, t, pool, tenant, reviewID))
	// reject still emits a resolved event (decision=reject).
	require.Equal(t, 1, countOutbox(ctx, t, pool, tenant, "dms.review.resolved.v1"))
}

// ---- local helpers ---------------------------------------------------------

func reviewStatus(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant, reviewID uuid.UUID) string {
	t.Helper()
	var s string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT status FROM review_queue WHERE tenant_id = $1 AND id = $2`,
		tenant, reviewID).Scan(&s))
	return s
}

func countOutbox(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant uuid.UUID, eventType string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM outbox WHERE tenant_id = $1 AND event_type = $2`,
		tenant, eventType).Scan(&n))
	return n
}
