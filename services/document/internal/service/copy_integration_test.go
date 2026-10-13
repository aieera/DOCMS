//go:build integration

// M-6: CopyDocument pointed the copy's current_version_id at the SOURCE's
// version row and created none of its own, so the copy's content 404'd via
// decrypt-stream (which binds version → document), ListVersions was empty,
// no blob reference was taken, and purging the source either failed on the
// head-pointer FK or (pre-DEC-8) destroyed the copy's bytes.
package service_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/document/internal/service"
)

// headResolves counts the document's head version that belongs to the
// document itself and joins a live blob — what decrypt-stream serves.
func headResolves(t *testing.T, h *harness, docID uuid.UUID) int {
	t.Helper()
	return countRows(t, h, `
		SELECT count(*)
		  FROM documents d
		  JOIN document_versions v
		    ON v.tenant_id = d.tenant_id AND v.id = d.current_version_id AND v.document_id = d.id
		  JOIN content_blobs b ON b.tenant_id = v.tenant_id AND b.id = v.content_blob_id
		 WHERE d.tenant_id = $1 AND d.id = $2`, h.tenant, docID)
}

func TestCopyDocument_ContentReadable(t *testing.T) {
	h := newHarness(t, true)
	wsID := uuid.Must(uuid.NewV7())
	f := mustCreateFolder(t, h, wsID, "Copy")
	src := mustDocIn(t, h, h.svc, wsID, f.ID, "original")
	blob := seedUploadedBlob(t, h, h.user, "us-east-1")
	srcV := mustLinkVersion(t, h, h.svc, src.ID, blob)

	cp, err := h.svc.CopyDocument(h.ctx, &service.CopyDocumentInput{DocumentID: src.ID, TargetFolderID: f.ID, CopiedBy: h.user})
	require.NoError(t, err)
	require.NotNil(t, cp.CurrentVersionID)
	require.NotEqual(t, srcV.ID, *cp.CurrentVersionID, "the copy owns its own version row")

	page, err := h.svc.ListVersions(h.ctx, cp.ID, 50, "")
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, blob.ID, page.Items[0].ContentBlobID)
	require.Equal(t, 1, page.Items[0].VersionNumber)
	require.Equal(t, 1, headResolves(t, h, cp.ID), "the copy's head serves its bytes")

	got, _, err := h.svc.GetDocument(h.ctx, cp.ID)
	require.NoError(t, err)
	require.Equal(t, int32(1), got.VersionCount)
	require.Equal(t, blob.SHA, got.SHA256Hash)
	require.Equal(t, 2, blobRefCount(t, h, blob.ID), "the copy takes a reference on the shared blob")
}

func TestCopyDocument_SurvivesSourcePurge(t *testing.T) {
	h := newHarness(t, true)
	s3 := newRecordingS3(t)
	s3.install(t, h.svc)
	wsID := uuid.Must(uuid.NewV7())
	f := mustCreateFolder(t, h, wsID, "Copy")
	src := mustDocIn(t, h, h.svc, wsID, f.ID, "original")
	blob := seedUploadedBlob(t, h, h.user, "us-east-1")
	mustLinkVersion(t, h, h.svc, src.ID, blob)
	cp, err := h.svc.CopyDocument(h.ctx, &service.CopyDocumentInput{DocumentID: src.ID, TargetFolderID: f.ID, CopiedBy: h.user})
	require.NoError(t, err)

	require.NoError(t, h.svc.DeleteDocument(h.ctx, src.ID))
	require.NoError(t, h.svc.PurgeDocument(h.ctx, src.ID))

	require.Empty(t, s3.deletedObjects(), "the copy's bytes survive the source's purge")
	require.True(t, blobRowExists(t, h, blob.ID))
	require.Equal(t, 1, blobRefCount(t, h, blob.ID))
	require.Equal(t, 1, headResolves(t, h, cp.ID))
}
