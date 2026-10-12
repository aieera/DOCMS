//go:build integration

// C-7 / DEC-8: a permanent delete (document purge, folder purge, Empty
// Trash) may only destroy blob bytes no other document still references, and
// must decide that — and drop the rows — in one transaction with the document
// locked, deleting S3 objects only after commit. Earlier purge tests used
// version-less documents; these all carry real versions.
package service_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/document/internal/service"
)

func TestPurgeDocument_SharedBlobSurvives(t *testing.T) {
	h := newHarness(t, true)
	s3 := newRecordingS3(t)
	s3.install(t, h.svc)
	wsID := uuid.Must(uuid.NewV7())
	f := mustCreateFolder(t, h, wsID, "Shared")
	a := mustDocIn(t, h, h.svc, wsID, f.ID, "A")
	b := mustDocIn(t, h, h.svc, wsID, f.ID, "B")

	// Both documents point at ONE blob whose reference_count was never
	// bumped for the second link (relink paths historically skipped it), so
	// the count alone would call it garbage after A's purge.
	blob := seedUploadedBlob(t, h, h.user, "us-east-1")
	mustLinkVersion(t, h, h.svc, a.ID, blob)
	bv := mustLinkVersion(t, h, h.svc, b.ID, blob)

	require.NoError(t, h.svc.DeleteDocument(h.ctx, a.ID))
	require.NoError(t, h.svc.PurgeDocument(h.ctx, a.ID))

	require.Empty(t, s3.deletedObjects(), "bytes still referenced by B must not be deleted")
	require.True(t, blobRowExists(t, h, blob.ID))
	require.GreaterOrEqual(t, blobRefCount(t, h, blob.ID), 1, "a referenced blob must not look like reaper garbage")
	require.Equal(t, 1, countRows(t, h, `
		SELECT count(*) FROM document_versions v
		  JOIN content_blobs c ON c.tenant_id = v.tenant_id AND c.id = v.content_blob_id
		 WHERE v.tenant_id = $1 AND v.id = $2`, h.tenant, bv.ID), "B's version still resolves its bytes")
	require.Zero(t, countRows(t, h, `SELECT count(*) FROM documents WHERE tenant_id = $1 AND id = $2`, h.tenant, a.ID))

	// Once the last referencing document is purged the blob is garbage: the
	// row goes and the bytes are deleted exactly once.
	require.NoError(t, h.svc.DeleteDocument(h.ctx, b.ID))
	require.NoError(t, h.svc.PurgeDocument(h.ctx, b.ID))
	require.Equal(t, []string{blob.Object()}, s3.deletedObjects())
	require.False(t, blobRowExists(t, h, blob.ID))
}

func TestPurgeDocument_VersionedDocSucceeds(t *testing.T) {
	h := newHarness(t, true)
	s3 := newRecordingS3(t)
	s3.install(t, h.svc)
	wsID := uuid.Must(uuid.NewV7())
	f := mustCreateFolder(t, h, wsID, "Versioned")
	doc := mustDocIn(t, h, h.svc, wsID, f.ID, "two versions")
	b1 := seedUploadedBlob(t, h, h.user, "us-east-1")
	b2 := seedUploadedBlob(t, h, h.user, "us-east-1")
	mustLinkVersion(t, h, h.svc, doc.ID, b1)
	mustLinkVersion(t, h, h.svc, doc.ID, b2)

	require.NoError(t, h.svc.DeleteDocument(h.ctx, doc.ID))
	require.NoError(t, h.svc.PurgeDocument(h.ctx, doc.ID))

	require.Zero(t, countRows(t, h, `SELECT count(*) FROM documents WHERE tenant_id = $1 AND id = $2`, h.tenant, doc.ID))
	require.Zero(t, countRows(t, h, `SELECT count(*) FROM document_versions WHERE tenant_id = $1 AND document_id = $2`, h.tenant, doc.ID))
	require.False(t, blobRowExists(t, h, b1.ID))
	require.False(t, blobRowExists(t, h, b2.ID))
	require.ElementsMatch(t, []string{b1.Object(), b2.Object()}, s3.deletedObjects())
	require.Equal(t, 1, countRows(t, h,
		`SELECT count(*) FROM outbox WHERE tenant_id = $1 AND event_type = 'dms.document.purged.v1' AND aggregate_id = $2`,
		h.tenant, doc.ID))
}

func TestPurgeDocument_RestoredBetweenPhasesNotDeleted(t *testing.T) {
	h := newHarness(t, true)
	s3 := newRecordingS3(t)
	s3.install(t, h.svc)
	wsID := uuid.Must(uuid.NewV7())
	f := mustCreateFolder(t, h, wsID, "Race")
	doc := mustDocIn(t, h, h.svc, wsID, f.ID, "restored mid-purge")
	blob := seedUploadedBlob(t, h, h.user, "us-east-1")
	mustLinkVersion(t, h, h.svc, doc.ID, blob)
	require.NoError(t, h.svc.DeleteDocument(h.ctx, doc.ID))

	// A restore is in flight (row updated, not yet committed) when the purge
	// starts. The purge must wait for it and then see a live document.
	restore, err := h.pool.Begin(h.ctx)
	require.NoError(t, err)
	_, err = restore.Exec(h.ctx,
		`UPDATE documents SET deleted_at = NULL WHERE tenant_id = $1 AND id = $2`, h.tenant, doc.ID)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() { done <- h.svc.PurgeDocument(h.ctx, doc.ID) }()
	require.True(t, waitForLockWaiter(t, h, 10*time.Second), "the purge must wait on the in-flight restore's row lock")
	require.NoError(t, restore.Commit(context.Background()))

	select {
	case err = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("purge did not finish")
	}
	require.Error(t, err)
	require.ErrorContains(t, err, "soft-deleted")
	require.Empty(t, s3.deletedObjects(), "no bytes may be deleted for a restored document")
	require.Equal(t, 1, countRows(t, h,
		`SELECT count(*) FROM documents WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`, h.tenant, doc.ID))
	require.Equal(t, 1, countRows(t, h,
		`SELECT count(*) FROM document_versions WHERE tenant_id = $1 AND document_id = $2`, h.tenant, doc.ID))
	require.True(t, blobRowExists(t, h, blob.ID))
}

func TestPurgeFolder_SharedBlobSurvives(t *testing.T) {
	h := newHarness(t, true)
	s3 := newRecordingS3(t)
	s3.install(t, h.svc)
	root, docs := mustCreateTree(t, h)

	// A live document outside the tree shares the blob of a document inside.
	otherWS := uuid.Must(uuid.NewV7())
	outside := mustCreateFolder(t, h, otherWS, "Outside")
	live := mustDocIn(t, h, h.svc, otherWS, outside.ID, "live sharer")
	shared := seedUploadedBlob(t, h, h.user, "us-east-1")
	own := seedUploadedBlob(t, h, h.user, "us-east-1")
	mustLinkVersion(t, h, h.svc, docs[0].ID, shared)
	mustLinkVersion(t, h, h.svc, docs[1].ID, own)
	lv := mustLinkVersion(t, h, h.svc, live.ID, shared)

	require.NoError(t, h.svc.DeleteFolder(h.ctx, root.ID))
	res, err := h.svc.PurgeFolder(h.ctx, root.ID)
	require.NoError(t, err)
	require.Equal(t, 2, res.DocumentsDeleted)
	require.Equal(t, 1, res.BlobsReleased)

	require.Equal(t, []string{own.Object()}, s3.deletedObjects(), "only the tree's exclusive bytes go")
	require.True(t, blobRowExists(t, h, shared.ID))
	require.False(t, blobRowExists(t, h, own.ID))
	require.Equal(t, 1, countRows(t, h, `
		SELECT count(*) FROM document_versions v
		  JOIN content_blobs c ON c.tenant_id = v.tenant_id AND c.id = v.content_blob_id
		 WHERE v.tenant_id = $1 AND v.id = $2`, h.tenant, lv.ID))
}

func TestEmptyTrash_DoesNotDeleteSharedBytes(t *testing.T) {
	h := newHarness(t, true)
	s3 := newRecordingS3(t)
	s3.install(t, h.svc)
	wsID := uuid.Must(uuid.NewV7())
	f := mustCreateFolder(t, h, wsID, "Trash")
	trashedShared := mustDocIn(t, h, h.svc, wsID, f.ID, "trashed, shares bytes")
	trashedOwn := mustDocIn(t, h, h.svc, wsID, f.ID, "trashed, own bytes")
	live := mustDocIn(t, h, h.svc, wsID, f.ID, "live")
	shared := seedUploadedBlob(t, h, h.user, "us-east-1")
	own := seedUploadedBlob(t, h, h.user, "us-east-1")
	mustLinkVersion(t, h, h.svc, trashedShared.ID, shared)
	mustLinkVersion(t, h, h.svc, trashedOwn.ID, own)
	mustLinkVersion(t, h, h.svc, live.ID, shared)
	require.NoError(t, h.svc.DeleteDocument(h.ctx, trashedShared.ID))
	require.NoError(t, h.svc.DeleteDocument(h.ctx, trashedOwn.ID))

	res, err := h.svc.EmptyTrash(h.ctx)
	require.NoError(t, err)
	require.Equal(t, 2, res.PurgedDocuments)
	require.Empty(t, res.Skipped)

	require.Equal(t, []string{own.Object()}, s3.deletedObjects())
	require.True(t, blobRowExists(t, h, shared.ID))
	require.Equal(t, 1, countRows(t, h,
		`SELECT count(*) FROM documents WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`, h.tenant, live.ID))
}

// The post-commit object cleanup must not hang off the request: a client
// that disconnects during Empty Trash used to stop it after the rows were
// gone, leaving bytes nothing could ever find again. Cleanup now runs
// detached, and whatever it cannot finish stays queued as a
// reference_count-0 blob row for the storage blob reaper.
func TestPurgeDocument_ObjectDeleteSurvivesCancelledContext(t *testing.T) {
	h := newHarness(t, true)
	s3 := newRecordingS3(t)
	s3.install(t, h.svc)
	wsID := uuid.Must(uuid.NewV7())
	f := mustCreateFolder(t, h, wsID, "Cancel")
	doc := mustDocIn(t, h, h.svc, wsID, f.ID, "two blobs")
	b1 := seedUploadedBlob(t, h, h.user, "us-east-1")
	b2 := seedUploadedBlob(t, h, h.user, "us-east-1")
	mustLinkVersion(t, h, h.svc, doc.ID, b1)
	mustLinkVersion(t, h, h.svc, doc.ID, b2)
	require.NoError(t, h.svc.DeleteDocument(h.ctx, doc.ID))

	// The caller goes away as soon as the first object delete reaches S3.
	reqCtx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	s3.setOnDelete(cancel)
	require.NoError(t, h.svc.PurgeDocument(reqCtx, doc.ID))
	s3.setOnDelete(nil)

	require.ElementsMatch(t, []string{b1.Object(), b2.Object()}, s3.deletedObjects(),
		"every released object is deleted even though the request context was cancelled")
	require.False(t, blobRowExists(t, h, b1.ID))
	require.False(t, blobRowExists(t, h, b2.ID))

	// S3 refusing the delete: the purge still completes, and the blob row
	// stays behind at reference_count 0 with no references — the storage
	// blob reaper's queue — instead of vanishing with its bytes still in S3.
	doc2 := mustDocIn(t, h, h.svc, wsID, f.ID, "s3 down")
	b3 := seedUploadedBlob(t, h, h.user, "us-east-1")
	mustLinkVersion(t, h, h.svc, doc2.ID, b3)
	require.NoError(t, h.svc.DeleteDocument(h.ctx, doc2.ID))
	s3.failDeletes.Store(true)
	require.NoError(t, h.svc.PurgeDocument(h.ctx, doc2.ID))
	require.Zero(t, countRows(t, h, `SELECT count(*) FROM documents WHERE tenant_id = $1 AND id = $2`, h.tenant, doc2.ID))
	require.True(t, blobRowExists(t, h, b3.ID), "the undeleted object stays recorded")
	require.Equal(t, 0, blobRefCount(t, h, b3.ID))
	require.Zero(t, countRows(t, h,
		`SELECT count(*) FROM document_versions WHERE tenant_id = $1 AND content_blob_id = $2`, h.tenant, b3.ID))
}

// seedTrashedDoc creates a document with one version per blob, soft-deletes
// it and returns its id.
func seedTrashedDoc(t *testing.T, h *harness, title string, blobs ...seededBlob) uuid.UUID {
	t.Helper()
	wsID := uuid.Must(uuid.NewV7())
	f := mustCreateFolder(t, h, wsID, title)
	doc := mustDocIn(t, h, h.svc, wsID, f.ID, title)
	for _, b := range blobs {
		mustLinkVersion(t, h, h.svc, doc.ID, b)
	}
	require.NoError(t, h.svc.DeleteDocument(h.ctx, doc.ID))
	return doc.ID
}

// requireQueued asserts a released blob is still recorded — row present at
// reference_count 0 with no version references, i.e. in the storage blob
// reaper's queue — and that its bytes were not touched.
func requireQueued(t *testing.T, h *harness, s3 *recordingS3, b seededBlob) {
	t.Helper()
	require.True(t, blobRowExists(t, h, b.ID), "row for %s must survive", b.Object())
	require.Equal(t, 0, blobRefCount(t, h, b.ID))
	require.NotContains(t, s3.deletedObjects(), b.Object(), "bytes of a surviving row must survive")
}

// Bytes may only be deleted once their row is certain to go: an S3 error
// must leave both, and so must a failure deleting the row (the row delete
// now precedes the S3 call, so S3 is never reached).
func TestPurgeDocument_S3FailureKeepsRowAndBytes(t *testing.T) {
	h := newHarness(t, true)
	s3 := newRecordingS3(t)
	s3.install(t, h.svc)

	refused := seedUploadedBlob(t, h, h.user, "us-east-1")
	docA := seedTrashedDoc(t, h, "s3 refuses", refused)
	s3.failDeletes.Store(true)
	require.NoError(t, h.svc.PurgeDocument(h.ctx, docA))
	s3.failDeletes.Store(false)
	requireQueued(t, h, s3, refused)

	stuck := seedUploadedBlob(t, h, h.user, "us-east-1")
	docB := seedTrashedDoc(t, h, "row delete fails", stuck)
	_, err := h.pool.Exec(h.ctx, fmt.Sprintf(`
		CREATE FUNCTION doc1_fail_blob_delete() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
		  IF OLD.id = '%s'::uuid THEN RAISE EXCEPTION 'injected row delete failure'; END IF;
		  RETURN OLD;
		END $$;
		CREATE TRIGGER doc1_fail_blob_delete BEFORE DELETE ON content_blobs
		  FOR EACH ROW EXECUTE FUNCTION doc1_fail_blob_delete();`, stuck.ID))
	require.NoError(t, err)
	require.NoError(t, h.svc.PurgeDocument(h.ctx, docB))
	requireQueued(t, h, s3, stuck)
	require.Zero(t, countRows(t, h, `SELECT count(*) FROM documents WHERE tenant_id = $1 AND id = $2`, h.tenant, docB))
}

// The cleanup budget must stop the loop BETWEEN blobs, never cancel a blob
// mid-way: every row left behind still has its bytes, every deleted object
// has lost its row.
func TestPurgeDocument_BudgetExhaustionLeavesRowsQueued(t *testing.T) {
	h := newHarness(t, true)
	s3 := newRecordingS3(t)
	s3.install(t, h.svc)
	restore := service.SetPurgeObjectDeleteBudget(500 * time.Millisecond)
	defer restore()

	blobs := make([]seededBlob, 6)
	for i := range blobs {
		blobs[i] = seedUploadedBlob(t, h, h.user, "us-east-1")
	}
	doc := seedTrashedDoc(t, h, "many blobs", blobs...)
	s3.setOnDelete(func() { time.Sleep(300 * time.Millisecond) }) // a slow S3
	require.NoError(t, h.svc.PurgeDocument(h.ctx, doc))
	s3.setOnDelete(nil)

	deleted := s3.deletedObjects()
	require.NotEmpty(t, deleted, "the fast path ran")
	require.Less(t, len(deleted), len(blobs), "the budget stopped the loop")
	for _, b := range blobs {
		if blobRowExists(t, h, b.ID) {
			requireQueued(t, h, s3, b)
		} else {
			require.Contains(t, deleted, b.Object(), "a deleted row's bytes were deleted")
		}
	}
}

// An object-locked (WORM) bucket ignores an unversioned DeleteObject (it
// writes a delete marker): dropping the row would destroy the wrapped DEK of
// retained bytes. The fast path skips such blobs, as the storage reaper does.
func TestPurgeDocument_WORMBucketSkipped(t *testing.T) {
	h := newHarness(t, true)
	s3 := newRecordingS3(t)
	s3.install(t, h.svc)
	locked := seedUploadedBlob(t, h, h.user, "us-east-1")
	_, err := h.pool.Exec(h.ctx,
		`UPDATE content_blobs SET storage_bucket = 'dms-us-east-1-worm' WHERE tenant_id = $1 AND id = $2`,
		h.tenant, locked.ID)
	require.NoError(t, err)
	locked.Bucket = "dms-us-east-1-worm"
	doc := seedTrashedDoc(t, h, "worm", locked)

	require.NoError(t, h.svc.PurgeDocument(h.ctx, doc))
	require.Empty(t, s3.deletedObjects())
	requireQueued(t, h, s3, locked)
}
