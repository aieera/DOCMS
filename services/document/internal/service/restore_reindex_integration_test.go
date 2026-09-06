//go:build integration
// +build integration

// Restore symmetry for the search index (QA SD-02): deleting a document
// removes it from OpenSearch (dms.document.deleted.v1 → index delete),
// but restoring it from Trash emitted only dms.document.restored.v1,
// which nothing indexes — so a restored document silently vanished from
// search forever. RestoreDocument must re-emit the full search
// projection (dms.document.reindexed.v1, same repair path as the admin
// reindex endpoint) in the same transaction as the restore.
package service_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/document/internal/service"
)

func TestRestoreDocument_ReemitsSearchProjection(t *testing.T) {
	h := newHarness(t, true)
	wsID := uuid.Must(uuid.NewV7())
	folder := mustCreateFolder(t, h, wsID, "Root")
	doc, err := h.svc.CreateDocument(h.ctx, &service.CreateDocumentInput{
		UpdatedBy: h.user, WorkspaceID: wsID, FolderID: folder.ID,
		Title: "Restored doc", RegionPin: "us-east-1",
	})
	require.NoError(t, err)

	require.NoError(t, h.svc.DeleteDocument(h.ctx, doc.ID))
	require.NoError(t, h.svc.RestoreDocument(h.ctx, doc.ID))

	// The restore must have queued the full-projection reindex event.
	payload := outboxPayload(t, h, "dms.document.reindexed.v1", doc.ID)
	require.Equal(t, doc.ID.String(), payload["document_id"])
	require.Equal(t, "Restored doc", payload["title"])
}

func TestRestoreFolder_ReemitsSearchProjectionForDocs(t *testing.T) {
	h := newHarness(t, true)
	wsID := uuid.Must(uuid.NewV7())
	folder := mustCreateFolder(t, h, wsID, "Root")
	doc, err := h.svc.CreateDocument(h.ctx, &service.CreateDocumentInput{
		UpdatedBy: h.user, WorkspaceID: wsID, FolderID: folder.ID,
		Title: "Cascade doc", RegionPin: "us-east-1",
	})
	require.NoError(t, err)

	// Cascade delete removes every cohort document from the index
	// (onFolderDeleted); restoring the folder must re-emit each one.
	require.NoError(t, h.svc.DeleteFolder(h.ctx, folder.ID))
	require.NoError(t, h.svc.RestoreFolder(h.ctx, folder.ID))

	payload := outboxPayload(t, h, "dms.document.reindexed.v1", doc.ID)
	require.Equal(t, doc.ID.String(), payload["document_id"])
	require.Equal(t, "Cascade doc", payload["title"])
}
