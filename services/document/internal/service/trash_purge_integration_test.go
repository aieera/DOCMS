//go:build integration

// Folder permanent delete (cohort purge) + Empty Trash. Companion to
// trash_retention_test.go — same harness, same real-Postgres setup.
// The docs created here have no uploaded versions, so the S3 phase of
// the purge is a no-op (zero blobs) and a client pointed at a dead
// endpoint is safe: these tests exercise the row/guard semantics, not
// object deletion (purge_cascade_integration_test.go covers the FK
// cascade side).
package service_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/storage"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// withDummyS3 satisfies the purge paths' s3-configured precondition.
// Never dialed in these tests (no blobs exist).
func withDummyS3(t *testing.T, h *harness) {
	t.Helper()
	s3c, err := storage.NewS3Client("127.0.0.1:1", "test", "test", false)
	require.NoError(t, err)
	h.svc.SetS3Client(s3c)
}

// mustCreateTree builds ws → root folder → child folder, with one doc
// in each folder. Returns the root and the two docs.
func mustCreateTree(t *testing.T, h *harness) (root *model.Folder, docs []*model.Document) {
	t.Helper()
	wsID := uuid.Must(uuid.NewV7())
	root = mustCreateFolder(t, h, wsID, "PurgeRoot")
	child, err := h.svc.CreateFolder(h.ctx, &service.CreateFolderInput{
		WorkspaceID:    wsID,
		Name:           "PurgeChild",
		ParentFolderID: &root.ID,
	})
	require.NoError(t, err)
	for _, f := range []*model.Folder{root, child} {
		doc, dErr := h.svc.CreateDocument(h.ctx, &service.CreateDocumentInput{
			UpdatedBy:   h.user,
			WorkspaceID: wsID,
			FolderID:    f.ID,
			Title:       "doc-in-" + f.Name,
			RegionPin:   "us-east-1",
		})
		require.NoError(t, dErr)
		docs = append(docs, doc)
	}
	return root, docs
}

func countRows(t *testing.T, h *harness, q string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, h.pool.QueryRow(h.ctx, q, args...).Scan(&n))
	return n
}

func TestPurgeFolder_RemovesCohortRows(t *testing.T) {
	h := newHarness(t, true)
	withDummyS3(t, h)
	root, docs := mustCreateTree(t, h)

	require.NoError(t, h.svc.DeleteFolder(h.ctx, root.ID))

	res, err := h.svc.PurgeFolder(h.ctx, root.ID)
	require.NoError(t, err)
	require.Equal(t, 2, res.FoldersDeleted)
	require.Equal(t, 2, res.DocumentsDeleted)

	require.Zero(t, countRows(t, h,
		`SELECT count(*) FROM folders WHERE tenant_id = $1 AND id = ANY($2)`,
		h.tenant, []uuid.UUID{root.ID}))
	for _, d := range docs {
		require.Zero(t, countRows(t, h,
			`SELECT count(*) FROM documents WHERE tenant_id = $1 AND id = $2`,
			h.tenant, d.ID))
	}
	require.Equal(t, 1, countRows(t, h,
		`SELECT count(*) FROM outbox WHERE tenant_id = $1 AND event_type = 'dms.folder.purged.v1'`,
		h.tenant))
}

func TestPurgeFolder_RefusesLiveFolder(t *testing.T) {
	h := newHarness(t, true)
	withDummyS3(t, h)
	root, _ := mustCreateTree(t, h)

	_, err := h.svc.PurgeFolder(h.ctx, root.ID)
	require.Error(t, err)
	require.ErrorContains(t, err, "soft-deleted")
}

func TestPurgeFolder_BlockedByActiveRetention(t *testing.T) {
	h := newHarness(t, true)
	withDummyS3(t, h)
	root, docs := mustCreateTree(t, h)
	require.NoError(t, h.svc.DeleteFolder(h.ctx, root.ID))
	setRetention(t, h, docs[0].ID, time.Now().Add(48*time.Hour), false)

	_, err := h.svc.PurgeFolder(h.ctx, root.ID)
	require.Error(t, err)
	require.ErrorContains(t, err, "blocked")

	// Fail-closed: nothing was deleted, not even the unblocked doc.
	require.Equal(t, 1, countRows(t, h,
		`SELECT count(*) FROM documents WHERE tenant_id = $1 AND id = $2`,
		h.tenant, docs[1].ID))
	require.Equal(t, 1, countRows(t, h,
		`SELECT count(*) FROM folders WHERE tenant_id = $1 AND id = $2`,
		h.tenant, root.ID))
}

func TestPurgeFolder_RefusesWhenDocRestoredOut(t *testing.T) {
	h := newHarness(t, true)
	withDummyS3(t, h)
	root, docs := mustCreateTree(t, h)
	require.NoError(t, h.svc.DeleteFolder(h.ctx, root.ID))

	// Restore one doc individually — it's live again, still pointing at
	// a trashed folder. Purging that folder must refuse, not orphan it.
	require.NoError(t, h.svc.RestoreDocument(h.ctx, docs[0].ID))

	_, err := h.svc.PurgeFolder(h.ctx, root.ID)
	require.Error(t, err)
	require.ErrorContains(t, err, "live document")
	require.Equal(t, 1, countRows(t, h,
		`SELECT count(*) FROM documents WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`,
		h.tenant, docs[0].ID))
}

func TestDeleteDocument_StampsDeletedBy(t *testing.T) {
	h := newHarness(t, true)
	doc := mustCreateDocumentWith(t, h, model.StateDraft)
	require.NoError(t, h.svc.DeleteDocument(h.ctx, doc.ID))

	var deletedBy *uuid.UUID
	require.NoError(t, h.pool.QueryRow(h.ctx,
		`SELECT deleted_by FROM documents WHERE tenant_id = $1 AND id = $2`,
		h.tenant, doc.ID).Scan(&deletedBy))
	require.NotNil(t, deletedBy)
	require.Equal(t, h.user, *deletedBy)

	page, err := h.svc.ListTrash(h.ctx, 50, "")
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.NotNil(t, page.Items[0].DeletedBy)
	require.Equal(t, h.user, *page.Items[0].DeletedBy)
	require.NotEmpty(t, page.Items[0].DeletedByName)
}

func TestEmptyTrash_PurgesEverythingPurgeable(t *testing.T) {
	h := newHarness(t, true)
	withDummyS3(t, h)

	// A folder cohort (2 folders, 2 docs) …
	root, _ := mustCreateTree(t, h)
	require.NoError(t, h.svc.DeleteFolder(h.ctx, root.ID))
	// … an individually deleted doc …
	loneDoc := mustCreateDocumentWith(t, h, model.StateDraft)
	require.NoError(t, h.svc.DeleteDocument(h.ctx, loneDoc.ID))
	// … and a retention-blocked deleted doc that must survive.
	heldDoc := mustCreateDocumentWith(t, h, model.StateDraft)
	require.NoError(t, h.svc.DeleteDocument(h.ctx, heldDoc.ID))
	setRetention(t, h, heldDoc.ID, time.Now().Add(48*time.Hour), false)

	res, err := h.svc.EmptyTrash(h.ctx)
	require.NoError(t, err)
	require.Equal(t, 1, res.PurgedFolders)
	require.Equal(t, 3, res.PurgedDocuments) // 2 cohort + 1 lone
	require.Len(t, res.Skipped, 1)
	require.Equal(t, heldDoc.ID, res.Skipped[0].ID)
	require.Equal(t, "document", res.Skipped[0].Type)

	require.Equal(t, 1, countRows(t, h,
		`SELECT count(*) FROM documents WHERE tenant_id = $1 AND deleted_at IS NOT NULL`,
		h.tenant))
	require.Zero(t, countRows(t, h,
		`SELECT count(*) FROM folders WHERE tenant_id = $1 AND deleted_at IS NOT NULL`,
		h.tenant))
}

func TestEmptyTrash_EmptyTrashIsANoop(t *testing.T) {
	h := newHarness(t, true)
	withDummyS3(t, h)
	res, err := h.svc.EmptyTrash(h.ctx)
	require.NoError(t, err)
	require.Zero(t, res.PurgedFolders)
	require.Zero(t, res.PurgedDocuments)
	require.Empty(t, res.Skipped)
}
