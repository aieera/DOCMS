//go:build integration
// +build integration

// Acceptance tests for Workstream 3 (pre-commit ingestion pipeline). These pin
// the spec's guarantees by driving the IngestAndRoute activity chain
// (ExtractKey → MatchExisting → Decide → Commit/NeedsReview) over a real
// Postgres, exactly as the Temporal workflow does:
//
//  1. high-confidence read matching an existing document → auto-committed as a
//     NEW VERSION of that document, with NO orphan staging document.
//  2. high-confidence read with no match → new document v1.
//  3. low-confidence / ambiguous read → review queue, NO version on any
//     document.
//  4. routing is idempotent — re-running commits exactly one version.
//
// Run with: go test -tags integration ./services/document/internal/service/...
//
// Reuses externalKeyFixture + seed/count helpers from external_key_upsert_test.go.
package service_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/document/internal/ingestroute"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// routeOutcome mirrors what the IngestAndRoute workflow does: it runs the four
// activities and branches on the decision. Returns ("committed"|"needs_review",
// commitResult, reviewID).
func routeOutcome(t *testing.T, svc *service.DocumentService, tenant, itemID uuid.UUID) (string, ingestroute.CommitResult, string) {
	t.Helper()
	acts := ingestroute.NewActivities(svc, zerolog.Nop())
	bg := context.Background()
	ek, err := acts.ExtractKey(bg, tenant.String(), itemID.String())
	require.NoError(t, err)
	if ek.Terminal {
		return "noop", ingestroute.CommitResult{}, ""
	}
	match, err := acts.MatchExisting(bg, tenant.String(), itemID.String())
	require.NoError(t, err)
	dec, err := acts.Decide(bg, ek.ExternalKey, ek.Confidence, match)
	require.NoError(t, err)
	if dec.Action == "commit" {
		c, cerr := acts.Commit(bg, tenant.String(), itemID.String())
		require.NoError(t, cerr)
		return "committed", c, ""
	}
	rid, rerr := acts.NeedsReview(bg, tenant.String(), itemID.String(), match, dec.Reason)
	require.NoError(t, rerr)
	return "needs_review", ingestroute.CommitResult{}, rid
}

func TestIngestRoute_HighConfMatch_AppendsVersionNoOrphan(t *testing.T) {
	callCtx, svc, pool, tenant, wsID, folderID := externalKeyFixture(t)
	ctx := context.Background()

	// An existing document keyed "INV-900" (version 1).
	const shaV1 = "1010101010101010101010101010101010101010101010101010101010101010"
	seedBlobSHA(ctx, t, pool, tenant, uuid.Must(uuid.NewV7()), shaV1, 1024, "application/pdf")
	doc, err := svc.UpsertDocumentByExternalKey(callCtx, &service.UpsertByExternalKeyInput{
		ExternalID: "INV-900", WorkspaceID: wsID, FolderID: folderID,
		BlobChecksum: shaV1, UpdatedBy: callerUser(t, callCtx),
	})
	require.NoError(t, err)
	require.True(t, doc.Created)

	// A NEW blob (different bytes) ingested for the same business key.
	const shaV2 = "2020202020202020202020202020202020202020202020202020202020202020"
	seedBlobSHA(ctx, t, pool, tenant, uuid.Must(uuid.NewV7()), shaV2, 2048, "application/pdf")
	item, err := svc.CreateIngestionItem(callCtx, &service.CreateIngestionInput{
		WorkspaceID: wsID, FolderID: folderID, TargetCustomerRef: "cust-A",
		BlobChecksum: shaV2, DocumentClass: "invoice",
	})
	require.NoError(t, err)
	markIngestionProcessed(ctx, t, pool, tenant, item.ID, "INV-900", 0.95)

	outcome, commit, _ := routeOutcome(t, svc, tenant, item.ID)
	require.Equal(t, "committed", outcome)
	require.False(t, commit.NewDocument, "high-confidence match must version the existing doc, not mint a new one")
	require.Equal(t, doc.DocumentID.String(), commit.DocumentID)

	// No orphan staging document, exactly two versions on the matched doc.
	require.Equal(t, 1, countDocs(ctx, t, pool, tenant, "INV-900"), "no orphan document created")
	require.Equal(t, 2, countVersions(ctx, t, pool, tenant, doc.DocumentID), "ingest appended version 2")
	require.Equal(t, string(model.IngestCommitted), ingestionStatus(ctx, t, pool, tenant, item.ID))
}

func TestIngestRoute_HighConfNoMatch_NewDocumentV1(t *testing.T) {
	callCtx, svc, pool, tenant, wsID, folderID := externalKeyFixture(t)
	ctx := context.Background()

	const sha = "3030303030303030303030303030303030303030303030303030303030303030"
	seedBlobSHA(ctx, t, pool, tenant, uuid.Must(uuid.NewV7()), sha, 1024, "application/pdf")
	item, err := svc.CreateIngestionItem(callCtx, &service.CreateIngestionInput{
		WorkspaceID: wsID, FolderID: folderID, TargetCustomerRef: "cust-B",
		BlobChecksum: sha, DocumentClass: "invoice",
	})
	require.NoError(t, err)
	markIngestionProcessed(ctx, t, pool, tenant, item.ID, "INV-FRESH", 0.97)

	outcome, commit, _ := routeOutcome(t, svc, tenant, item.ID)
	require.Equal(t, "committed", outcome)
	require.True(t, commit.NewDocument, "unseen key with high confidence mints a new document")
	require.Equal(t, 1, countDocs(ctx, t, pool, tenant, "INV-FRESH"))

	docID := uuid.MustParse(commit.DocumentID)
	require.Equal(t, 1, countVersions(ctx, t, pool, tenant, docID))
	require.Equal(t, string(model.IngestCommitted), ingestionStatus(ctx, t, pool, tenant, item.ID))
}

func TestIngestRoute_LowConfidence_ReviewQueueNoVersion(t *testing.T) {
	callCtx, svc, pool, tenant, wsID, folderID := externalKeyFixture(t)
	ctx := context.Background()

	const sha = "4040404040404040404040404040404040404040404040404040404040404040"
	seedBlobSHA(ctx, t, pool, tenant, uuid.Must(uuid.NewV7()), sha, 1024, "application/pdf")
	item, err := svc.CreateIngestionItem(callCtx, &service.CreateIngestionInput{
		WorkspaceID: wsID, FolderID: folderID, TargetCustomerRef: "cust-C",
		BlobChecksum: sha, DocumentClass: "invoice",
	})
	require.NoError(t, err)
	// Blurry read: a key was guessed but confidence is below the 0.85 gate.
	markIngestionProcessed(ctx, t, pool, tenant, item.ID, "INV-MAYBE", 0.42)

	outcome, _, reviewID := routeOutcome(t, svc, tenant, item.ID)
	require.Equal(t, "needs_review", outcome)
	require.NotEmpty(t, reviewID)

	// No version was written to ANY document for this read.
	require.Equal(t, 0, countDocs(ctx, t, pool, tenant, "INV-MAYBE"), "low-confidence read must not create a document")
	require.Equal(t, string(model.IngestNeedsReview), ingestionStatus(ctx, t, pool, tenant, item.ID))

	// It lands in the review queue, listable via the service surface.
	items, err := svc.ListReviewQueue(callCtx, string(model.ReviewPending), 50)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, item.ID, items[0].IngestionItemID)
	require.Equal(t, string(model.ReasonBelowThreshold), string(items[0].Reason))
}

func TestIngestRoute_CommitIsIdempotent(t *testing.T) {
	callCtx, svc, pool, tenant, wsID, folderID := externalKeyFixture(t)
	ctx := context.Background()

	const sha = "5050505050505050505050505050505050505050505050505050505050505050"
	seedBlobSHA(ctx, t, pool, tenant, uuid.Must(uuid.NewV7()), sha, 1024, "application/pdf")
	item, err := svc.CreateIngestionItem(callCtx, &service.CreateIngestionInput{
		WorkspaceID: wsID, FolderID: folderID, TargetCustomerRef: "cust-D",
		BlobChecksum: sha, DocumentClass: "invoice",
	})
	require.NoError(t, err)
	markIngestionProcessed(ctx, t, pool, tenant, item.ID, "INV-IDEM", 0.99)

	o1, c1, _ := routeOutcome(t, svc, tenant, item.ID)
	require.Equal(t, "committed", o1)
	docID := uuid.MustParse(c1.DocumentID)

	// Re-run the whole chain — the item is now terminal, so it must no-op.
	o2, _, _ := routeOutcome(t, svc, tenant, item.ID)
	require.Equal(t, "noop", o2)

	require.Equal(t, 1, countDocs(ctx, t, pool, tenant, "INV-IDEM"))
	require.Equal(t, 1, countVersions(ctx, t, pool, tenant, docID), "re-routing must not append a second version")
}

// ---- local helpers ---------------------------------------------------------

// markIngestionProcessed simulates the intelligence worker's processed-write
// (extracted key + confidence + status=processed) so the Go routing path can be
// exercised without the Python OCR engine.
func markIngestionProcessed(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant, itemID uuid.UUID, key string, conf float64) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		UPDATE ingestion_items
		   SET extracted_external_key = $3, confidence = $4,
		       status = 'processed', updated_at = now()
		 WHERE tenant_id = $1 AND id = $2`,
		tenant, itemID, key, conf)
	require.NoError(t, err)
}

func ingestionStatus(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant, itemID uuid.UUID) string {
	t.Helper()
	var s string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT status FROM ingestion_items WHERE tenant_id = $1 AND id = $2`,
		tenant, itemID).Scan(&s))
	return s
}
