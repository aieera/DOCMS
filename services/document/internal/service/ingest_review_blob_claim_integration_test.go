//go:build integration

// DEC-12 on the ingestion paths. A staged item's blob was supplied by its
// uploader, so the claim that matters when a reviewer files it is the
// uploader's: they completed the upload session that produced it (or the
// reviewer can view a document that already carries it). Staging a blob
// the caller has no claim on is refused up front; a staged item that points
// at someone else's bytes (a row staged before the claim rules existed)
// cannot be filed by review — NotFound, no version and no empty document —
// while a legitimate item is filed by a reviewer who is not its uploader.
package service_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/service"
)

func TestIngestReview_ForeignBlobRejected(t *testing.T) {
	h := newHarness(t, true)
	bg := context.Background()
	member := seedOtherUser(t, h)
	memberCtx := auth.WithUser(h.ctx, auth.UserInfo{ID: member, TenantID: h.tenant, Role: "member"})
	wsID := uuid.Must(uuid.NewV7())
	f := mustCreateFolder(t, h, wsID, "Inbox")
	target := mustDocIn(t, h, h.svc, wsID, f.ID, "target")
	docsInWS := func() int {
		return countRows(t, h, `SELECT count(*) FROM documents WHERE tenant_id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, h.tenant, wsID)
	}
	stageToReview := func(ctx context.Context, b seededBlob, key string) uuid.UUID {
		t.Helper()
		item, err := h.svc.CreateIngestionItem(ctx, &service.CreateIngestionInput{
			WorkspaceID: wsID, FolderID: f.ID, BlobChecksum: b.SHA, DocumentClass: "invoice",
		})
		require.NoError(t, err)
		markIngestionProcessed(bg, t, h.pool, h.tenant, item.ID, key, 0.30)
		outcome, _, reviewID := routeOutcome(t, h.svc, h.tenant, item.ID)
		require.Equal(t, "needs_review", outcome)
		return uuid.MustParse(reviewID)
	}

	// 1. Staging: a checksum of someone else's bytes is not stageable.
	foreign := seedUploadedBlob(t, h, member, "us-east-1")
	_, err := h.svc.CreateIngestionItem(h.ctx, &service.CreateIngestionInput{
		WorkspaceID: wsID, FolderID: f.ID, BlobChecksum: foreign.SHA,
	})
	require.ErrorContains(t, err, "no uploaded content blob matches this checksum")
	_, err = h.svc.CreateIngestionItem(h.ctx, &service.CreateIngestionInput{
		WorkspaceID: wsID, FolderID: f.ID, BlobChecksum: foreign.SHA, BlobRef: foreign.ID.String(),
	})
	require.ErrorContains(t, err, "no uploaded content blob matches this checksum")

	// 2. Review: a staged row pointing at foreign bytes cannot be filed.
	own := seedUploadedBlob(t, h, h.user, "us-east-1")
	reviewID := stageToReview(h.ctx, own, "INV-LEGACY")
	_, err = h.pool.Exec(h.ctx, `
		UPDATE ingestion_items SET blob_ref = $3, blob_checksum = $4
		 WHERE tenant_id = $1 AND id = (SELECT ingestion_item_id FROM review_queue WHERE tenant_id = $1 AND id = $2)`,
		h.tenant, reviewID, foreign.ID, foreign.SHA)
	require.NoError(t, err)
	before := docsInWS()
	_, err = h.svc.ResolveReviewItem(h.ctx, &service.ResolveReviewInput{
		ReviewItemID: reviewID, Decision: "new_version", TargetDocumentID: target.ID,
	})
	require.ErrorIs(t, err, vdmserr.ErrNotFound)
	_, err = h.svc.ResolveReviewItem(h.ctx, &service.ResolveReviewInput{
		ReviewItemID: reviewID, Decision: "new_document",
	})
	require.ErrorIs(t, err, vdmserr.ErrNotFound)
	require.Equal(t, before, docsInWS(), "a refused new_document leaves no empty document behind")
	require.Zero(t, countRows(t, h, `SELECT count(*) FROM document_versions WHERE tenant_id = $1 AND content_blob_id = $2`, h.tenant, foreign.ID))

	// 3. Control: the member stages their own upload; a reviewer who is not
	// the uploader files it.
	mine := seedUploadedBlob(t, h, member, "us-east-1")
	okReview := stageToReview(memberCtx, mine, "INV-OK")
	res, err := h.svc.ResolveReviewItem(h.ctx, &service.ResolveReviewInput{
		ReviewItemID: okReview, Decision: "new_version", TargetDocumentID: target.ID,
	})
	require.NoError(t, err)
	require.Equal(t, target.ID, res.DocumentID)
	require.Equal(t, 1, countRows(t, h, `SELECT count(*) FROM document_versions WHERE tenant_id = $1 AND content_blob_id = $2`, h.tenant, mine.ID))
}
