//go:build integration

// H-8 / DEC-7: a legal hold placed through the compliance API binds the
// document in legal_hold_documents without touching lifecycle_state. Every
// mutating path must honour that binding, not just the in-band legal_hold
// state; and the in-band apply_hold / release_hold actions must write and
// release real rows in that same schema.
package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/compliance"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// complianceHold binds docs to a new hold through the compliance API.
func complianceHold(t *testing.T, h *harness, docs ...uuid.UUID) uuid.UUID {
	t.Helper()
	hold, err := compliance.NewHoldsService(h.pool).Create(h.ctx, h.tenant, h.user, compliance.CreateInput{
		Name: "Matter " + uuid.NewString()[:8], DocumentIDs: docs,
	})
	require.NoError(t, err)
	return hold.ID
}

// heldDocWithVersion returns a document (one version) in folder f of wsID,
// bound to a compliance-API hold. Its lifecycle_state stays draft.
func heldDocWithVersion(t *testing.T, h *harness, wsID, folderID uuid.UUID) (*model.Document, *model.Version) {
	t.Helper()
	doc := mustDocIn(t, h, h.svc, wsID, folderID, "held")
	v := mustLinkVersion(t, h, h.svc, doc.ID, seedUploadedBlob(t, h, h.user, "us-east-1"))
	complianceHold(t, h, doc.ID)
	return doc, v
}

func TestCreateVersion_BindingHoldBlocks(t *testing.T) {
	h := newHarness(t, true)
	wsID := uuid.Must(uuid.NewV7())
	f := mustCreateFolder(t, h, wsID, "Hold")
	doc, _ := heldDocWithVersion(t, h, wsID, f.ID)

	_, err := h.svc.CreateVersion(h.ctx, &service.CreateVersionInput{
		DocumentID: doc.ID, ContentBlobID: seedUploadedBlob(t, h, h.user, "us-east-1").ID,
	})
	require.ErrorIs(t, err, vdmserr.ErrLegalHold)
	require.Equal(t, 1, countRows(t, h,
		`SELECT count(*) FROM document_versions WHERE tenant_id = $1 AND document_id = $2`, h.tenant, doc.ID))
}

func TestMove_BindingHoldBlocks(t *testing.T) {
	h := newHarness(t, true)
	wsID := uuid.Must(uuid.NewV7())
	f := mustCreateFolder(t, h, wsID, "Hold")
	dest := mustCreateFolder(t, h, wsID, "Elsewhere")
	doc, _ := heldDocWithVersion(t, h, wsID, f.ID)

	_, err := h.svc.MoveDocument(h.ctx, &service.MoveDocumentInput{DocumentID: doc.ID, TargetFolderID: dest.ID, UpdatedBy: h.user})
	require.ErrorIs(t, err, vdmserr.ErrLegalHold)
	require.Equal(t, 1, countRows(t, h,
		`SELECT count(*) FROM documents WHERE tenant_id = $1 AND id = $2 AND folder_id = $3`, h.tenant, doc.ID, f.ID))
}

func TestCopy_BindingHoldBlocks(t *testing.T) {
	h := newHarness(t, true)
	wsID := uuid.Must(uuid.NewV7())
	f := mustCreateFolder(t, h, wsID, "Hold")
	doc, _ := heldDocWithVersion(t, h, wsID, f.ID)

	_, err := h.svc.CopyDocument(h.ctx, &service.CopyDocumentInput{DocumentID: doc.ID, TargetFolderID: f.ID, CopiedBy: h.user})
	require.ErrorIs(t, err, vdmserr.ErrLegalHold)
	require.Equal(t, 1, countRows(t, h, `SELECT count(*) FROM documents WHERE tenant_id = $1`, h.tenant))
}

func TestRestoreVersion_BindingHoldBlocks(t *testing.T) {
	h := newHarness(t, true)
	wsID := uuid.Must(uuid.NewV7())
	f := mustCreateFolder(t, h, wsID, "Hold")
	doc := mustDocIn(t, h, h.svc, wsID, f.ID, "two versions")
	v1 := mustLinkVersion(t, h, h.svc, doc.ID, seedUploadedBlob(t, h, h.user, "us-east-1"))
	mustLinkVersion(t, h, h.svc, doc.ID, seedUploadedBlob(t, h, h.user, "us-east-1"))
	complianceHold(t, h, doc.ID)

	_, err := h.svc.RestoreVersion(h.ctx, doc.ID, v1.ID, "")
	require.ErrorIs(t, err, vdmserr.ErrLegalHold)
	require.Equal(t, 2, countRows(t, h,
		`SELECT count(*) FROM document_versions WHERE tenant_id = $1 AND document_id = $2`, h.tenant, doc.ID))
}

func TestUpsertAppend_BindingHoldBlocks(t *testing.T) {
	h := newHarness(t, true)
	wsID := uuid.Must(uuid.NewV7())
	f := mustCreateFolder(t, h, wsID, "ERP")
	first := seedUploadedBlob(t, h, h.user, "us-east-1")
	res, err := h.svc.UpsertDocumentByExternalKey(h.ctx, &service.UpsertByExternalKeyInput{
		ExternalID: "INV-HOLD-1", WorkspaceID: wsID, FolderID: f.ID, BlobChecksum: first.SHA, UpdatedBy: h.user,
	})
	require.NoError(t, err)
	complianceHold(t, h, res.DocumentID)

	second := seedUploadedBlob(t, h, h.user, "us-east-1")
	_, err = h.svc.UpsertDocumentByExternalKey(h.ctx, &service.UpsertByExternalKeyInput{
		ExternalID: "INV-HOLD-1", WorkspaceID: wsID, FolderID: f.ID, BlobChecksum: second.SHA, UpdatedBy: h.user,
	})
	require.ErrorIs(t, err, vdmserr.ErrLegalHold)
	require.Equal(t, 1, countRows(t, h,
		`SELECT count(*) FROM document_versions WHERE tenant_id = $1 AND document_id = $2`, h.tenant, res.DocumentID))
}

// The in-band hold round trip: apply_hold writes a real legal_holds row +
// binding the compliance API (and every gate) sees; release_hold restores
// the previous state and deactivates only that hold — a compliance-API hold
// on the same document stays in force until it is released through the
// compliance API.
func TestApplyHold_RoundTrip(t *testing.T) {
	h := newHarness(t, true)
	holds := compliance.NewHoldsService(h.pool)
	doc := mustCreateDocumentWith(t, h, model.StateActive)

	held, err := h.svc.UpdateLifecycle(h.ctx, &service.UpdateLifecycleInput{
		DocumentID: doc.ID, Action: model.ActionApplyHold,
		HoldName: "Smith v. Acme", HoldMatterReference: "M-42", Reason: "litigation",
	})
	require.NoError(t, err)
	require.Equal(t, model.StateLegalHold, held.LifecycleState)
	require.Equal(t, 1, countRows(t, h, `
		SELECT count(*) FROM legal_holds lh
		  JOIN legal_hold_documents lhd ON lhd.tenant_id = lh.tenant_id AND lhd.hold_id = lh.id
		 WHERE lh.tenant_id = $1 AND lhd.document_id = $2 AND lh.is_active
		   AND lh.origin = 'lifecycle' AND lh.name = 'Smith v. Acme' AND lh.matter_reference = 'M-42'
		   AND lhd.previous_lifecycle_state = 'active'`, h.tenant, doc.ID))
	bound, err := holds.AnyActiveHoldFor(h.ctx, h.tenant, doc.ID)
	require.NoError(t, err)
	require.True(t, bound, "the compliance API sees the in-band hold")
	listed, err := holds.List(h.ctx, h.tenant, compliance.ListFilter{Status: "active", DocumentID: doc.ID})
	require.NoError(t, err)
	require.Len(t, listed, 1)

	// A second, compliance-API hold on the same document.
	matter := complianceHold(t, h, doc.ID)

	released, err := h.svc.UpdateLifecycle(h.ctx, &service.UpdateLifecycleInput{
		DocumentID: doc.ID, Action: model.ActionReleaseHold, Reason: "settled",
	})
	require.NoError(t, err)
	require.Equal(t, model.StateActive, released.LifecycleState, "release restores the pre-hold state")
	require.Equal(t, 1, countRows(t, h, `
		SELECT count(*) FROM legal_holds
		 WHERE tenant_id = $1 AND origin = 'lifecycle' AND NOT is_active
		   AND released_by = $2 AND released_at IS NOT NULL`, h.tenant, h.user))

	// The compliance hold still binds: the document stays frozen.
	bound, err = holds.AnyActiveHoldFor(h.ctx, h.tenant, doc.ID)
	require.NoError(t, err)
	require.True(t, bound, "release_hold must not release a compliance-API hold")
	_, err = h.svc.UpdateLifecycle(h.ctx, &service.UpdateLifecycleInput{DocumentID: doc.ID, Action: model.ActionArchive})
	require.ErrorIs(t, err, vdmserr.ErrLegalHold)

	_, err = holds.Release(h.ctx, h.tenant, matter, h.user, compliance.ReleaseInput{Reason: "closed", Approver: h.user})
	require.NoError(t, err)
	_, err = h.svc.UpdateLifecycle(h.ctx, &service.UpdateLifecycleInput{DocumentID: doc.ID, Action: model.ActionArchive})
	require.NoError(t, err)
}

// lifecycleHoldID returns the id of the in-band hold apply_hold placed on doc.
func lifecycleHoldID(t *testing.T, h *harness, docID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, h.pool.QueryRow(h.ctx, `
		SELECT lh.id FROM legal_holds lh
		  JOIN legal_hold_documents lhd ON lhd.tenant_id = lh.tenant_id AND lhd.hold_id = lh.id
		 WHERE lh.tenant_id = $1 AND lhd.document_id = $2 AND lh.origin = 'lifecycle'`,
		h.tenant, docID).Scan(&id))
	return id
}

func applyHold(t *testing.T, h *harness, docID uuid.UUID) {
	t.Helper()
	_, err := h.svc.UpdateLifecycle(h.ctx, &service.UpdateLifecycleInput{
		DocumentID: docID, Action: model.ActionApplyHold, HoldName: "In-band " + uuid.NewString()[:8], Reason: "litigation",
	})
	require.NoError(t, err)
}

// A lifecycle hold belongs to its document: the compliance API may list it
// but must not re-scope or release it (that would strand the document in
// legal_hold, or free other documents without an approver).
func TestComplianceRelease_LifecycleHoldRefused(t *testing.T) {
	h := newHarness(t, true)
	holds := compliance.NewHoldsService(h.pool)
	doc := mustCreateDocumentWith(t, h, model.StateActive)
	other := mustCreateDocumentWith(t, h, model.StateActive)
	applyHold(t, h, doc.ID)
	holdID := lifecycleHoldID(t, h, doc.ID)

	_, err := holds.Release(h.ctx, h.tenant, holdID, h.user, compliance.ReleaseInput{Reason: "closed", Approver: h.user})
	require.Equal(t, vdmserr.KindConflict, vdmserr.KindOf(err), "release of a lifecycle hold via the compliance API is a 409")
	_, err = holds.Update(h.ctx, h.tenant, holdID, h.user, compliance.UpdateInput{AddDocs: []uuid.UUID{other.ID}})
	require.Equal(t, vdmserr.KindConflict, vdmserr.KindOf(err))
	_, err = holds.Update(h.ctx, h.tenant, holdID, h.user, compliance.UpdateInput{RemoveDocs: []uuid.UUID{doc.ID}})
	require.Equal(t, vdmserr.KindConflict, vdmserr.KindOf(err))

	got, err := holds.Get(h.ctx, h.tenant, holdID)
	require.NoError(t, err)
	require.True(t, got.IsActive)
	require.Equal(t, []string{doc.ID.String()}, got.DocumentIDs)

	// Compliance-API holds are unaffected.
	matter := complianceHold(t, h, other.ID)
	_, err = holds.Release(h.ctx, h.tenant, matter, h.user, compliance.ReleaseInput{Reason: "closed", Approver: h.user})
	require.NoError(t, err)
}

// release_hold on one document must not release the hold for any other
// document bound to the same hold row.
func TestReleaseHold_OnlyReleasesOwnBinding(t *testing.T) {
	h := newHarness(t, true)
	holds := compliance.NewHoldsService(h.pool)
	a := mustCreateDocumentWith(t, h, model.StateActive)
	b := mustCreateDocumentWith(t, h, model.StateActive)
	applyHold(t, h, a.ID)
	holdID := lifecycleHoldID(t, h, a.ID)
	// A second document bound to the same lifecycle hold row (the API
	// refuses this now; legacy or hand-made rows must still be safe).
	_, err := h.pool.Exec(h.ctx, `
		INSERT INTO legal_hold_documents (tenant_id, hold_id, document_id, previous_lifecycle_state)
		VALUES ($1, $2, $3, 'active')`, h.tenant, holdID, b.ID)
	require.NoError(t, err)

	released, err := h.svc.UpdateLifecycle(h.ctx, &service.UpdateLifecycleInput{
		DocumentID: a.ID, Action: model.ActionReleaseHold, Reason: "settled for A",
	})
	require.NoError(t, err)
	require.Equal(t, model.StateActive, released.LifecycleState)

	heldA, err := holds.AnyActiveHoldFor(h.ctx, h.tenant, a.ID)
	require.NoError(t, err)
	require.False(t, heldA, "A left the hold")
	heldB, err := holds.AnyActiveHoldFor(h.ctx, h.tenant, b.ID)
	require.NoError(t, err)
	require.True(t, heldB, "B is still held — releasing A must not release B")
	_, err = h.svc.UpdateLifecycle(h.ctx, &service.UpdateLifecycleInput{DocumentID: b.ID, Action: model.ActionArchive})
	require.ErrorIs(t, err, vdmserr.ErrLegalHold)
}

// DEC-7 on UpdateDocument: the binding hold is judged by the same
// allow-list as the legal_hold state — title and custom_metadata edits
// pass, description and tag edits are refused.
func TestUpdateDocument_BindingHoldBlocks(t *testing.T) {
	h := newHarness(t, true)
	wsID := uuid.Must(uuid.NewV7())
	f := mustCreateFolder(t, h, wsID, "Hold")
	doc, _ := heldDocWithVersion(t, h, wsID, f.ID)
	desc := "rewritten"
	title := "retitled"

	_, err := h.svc.UpdateDocument(h.ctx, &service.UpdateDocumentInput{DocumentID: doc.ID, Tags: []string{"x"}, UpdatedBy: h.user})
	require.ErrorIs(t, err, vdmserr.ErrLegalHold)
	_, err = h.svc.UpdateDocument(h.ctx, &service.UpdateDocumentInput{DocumentID: doc.ID, ClearTags: true, UpdatedBy: h.user})
	require.ErrorIs(t, err, vdmserr.ErrLegalHold)
	_, err = h.svc.UpdateDocument(h.ctx, &service.UpdateDocumentInput{DocumentID: doc.ID, Description: &desc, UpdatedBy: h.user})
	require.ErrorIs(t, err, vdmserr.ErrLegalHold)

	got, err := h.svc.UpdateDocument(h.ctx, &service.UpdateDocumentInput{
		DocumentID: doc.ID, Title: &title, CustomMetadata: map[string]any{"matter": "M-1"}, UpdatedBy: h.user,
	})
	require.NoError(t, err, "title and metadata edits stay allowed under hold")
	require.Equal(t, "retitled", got.Title)
	require.Equal(t, 1, countRows(t, h,
		`SELECT count(*) FROM documents WHERE tenant_id = $1 AND id = $2 AND description = '' AND tags = '{}'`,
		h.tenant, doc.ID))
}

// A hold binding and a version write on the same document serialize on the
// document row, so a version can never commit after a hold has committed.
func TestCreateVersion_HoldCommittedConcurrentlyBlocks(t *testing.T) {
	t.Run("hold binding in flight first", func(t *testing.T) {
		h := newHarness(t, true)
		wsID := uuid.Must(uuid.NewV7())
		f := mustCreateFolder(t, h, wsID, "Race")
		doc := mustDocIn(t, h, h.svc, wsID, f.ID, "race")
		blob := seedUploadedBlob(t, h, h.user, "us-east-1")

		// Park the compliance hold between locking the document and
		// writing its binding: block the binding INSERT with a table lock.
		blocker, err := h.pool.Begin(h.ctx)
		require.NoError(t, err)
		t.Cleanup(func() { _ = blocker.Rollback(context.Background()) })
		_, err = blocker.Exec(h.ctx, `LOCK TABLE legal_hold_documents IN EXCLUSIVE MODE`)
		require.NoError(t, err)
		holdDone := make(chan error, 1)
		go func() {
			_, herr := compliance.NewHoldsService(h.pool).Create(h.ctx, h.tenant, h.user, compliance.CreateInput{
				Name: "Race matter", DocumentIDs: []uuid.UUID{doc.ID},
			})
			holdDone <- herr
		}()
		require.True(t, waitForLockWaiters(t, h, 1, 10*time.Second), "hold binding parked")

		upload, blocked := runConcurrently(t, h, func() error {
			_, verr := h.svc.CreateVersion(h.ctx, &service.CreateVersionInput{DocumentID: doc.ID, ContentBlobID: blob.ID})
			return verr
		})
		require.True(t, blocked, "the version write must wait for the in-flight hold on the document row")
		require.NoError(t, blocker.Commit(context.Background()))
		require.NoError(t, recv(t, holdDone))
		require.ErrorIs(t, recv(t, upload), vdmserr.ErrLegalHold, "the hold committed first, so the version is refused")
		require.Zero(t, countRows(t, h,
			`SELECT count(*) FROM document_versions WHERE tenant_id = $1 AND document_id = $2`, h.tenant, doc.ID))
	})

	t.Run("version write in flight first", func(t *testing.T) {
		h := newHarness(t, true)
		gate := newGatedPolicy()
		defer gate.open()
		gated := serviceWith(h, gate)
		wsID := uuid.Must(uuid.NewV7())
		f := mustCreateFolder(t, h, wsID, "Race")
		doc := mustDocIn(t, h, h.svc, wsID, f.ID, "race")
		blob := seedUploadedBlob(t, h, h.user, "us-east-1")

		// The version write passes its hold check and parks before writing.
		gate.armed.Store(true)
		upload := make(chan error, 1)
		go func() {
			_, verr := gated.CreateVersion(h.ctx, &service.CreateVersionInput{DocumentID: doc.ID, ContentBlobID: blob.ID})
			upload <- verr
		}()
		awaitParked(t, gate)

		hold, blocked := runConcurrently(t, h, func() error {
			_, herr := compliance.NewHoldsService(h.pool).Create(h.ctx, h.tenant, h.user, compliance.CreateInput{
				Name: "Race matter", DocumentIDs: []uuid.UUID{doc.ID},
			})
			return herr
		})
		require.True(t, blocked, "the hold must not commit while a version write that already passed its check is in flight")
		gate.open()
		require.NoError(t, recv(t, upload), "the version was ordered before the hold")
		require.NoError(t, recv(t, hold))

		_, err := h.svc.CreateVersion(h.ctx, &service.CreateVersionInput{
			DocumentID: doc.ID, ContentBlobID: seedUploadedBlob(t, h, h.user, "us-east-1").ID,
		})
		require.ErrorIs(t, err, vdmserr.ErrLegalHold, "once the hold has committed no further version lands")
	})
}

// DEC-7 on lifecycle transitions: a hold being applied and a dispose of the
// same document serialize on the document row, so a held document can never
// be disposed (spoliation) whichever starts first.
func TestUpdateLifecycle_HoldAppliedConcurrentlyBlocksDispose(t *testing.T) {
	dispose := func(svc *service.DocumentService, h *harness, docID uuid.UUID) error {
		_, err := svc.UpdateLifecycle(h.ctx, &service.UpdateLifecycleInput{
			DocumentID: docID, Action: model.ActionDispose, Reason: "retention elapsed",
		})
		return err
	}

	t.Run("hold binding in flight first", func(t *testing.T) {
		h := newHarness(t, true)
		doc := mustCreateDocumentWith(t, h, model.StateArchived)

		// Park the compliance hold after it share-locked the document,
		// before it writes the binding.
		blocker, err := h.pool.Begin(h.ctx)
		require.NoError(t, err)
		t.Cleanup(func() { _ = blocker.Rollback(context.Background()) })
		_, err = blocker.Exec(h.ctx, `LOCK TABLE legal_hold_documents IN EXCLUSIVE MODE`)
		require.NoError(t, err)
		holdDone := make(chan error, 1)
		go func() {
			_, herr := compliance.NewHoldsService(h.pool).Create(h.ctx, h.tenant, h.user, compliance.CreateInput{
				Name: "Race matter", DocumentIDs: []uuid.UUID{doc.ID},
			})
			holdDone <- herr
		}()
		require.True(t, waitForLockWaiters(t, h, 1, 10*time.Second), "hold binding parked")

		disposed, blocked := runConcurrently(t, h, func() error { return dispose(h.svc, h, doc.ID) })
		require.True(t, blocked, "the dispose must wait for the in-flight hold on the document row")
		require.NoError(t, blocker.Commit(context.Background()))
		require.NoError(t, recv(t, holdDone))
		require.ErrorIs(t, recv(t, disposed), vdmserr.ErrLegalHold, "the hold committed first, so dispose is refused")

		got, _, err := h.svc.GetDocument(h.ctx, doc.ID)
		require.NoError(t, err)
		require.Equal(t, model.StateArchived, got.LifecycleState, "a held document is never disposed")
	})

	t.Run("dispose in flight first", func(t *testing.T) {
		h := newHarness(t, true)
		gate := newGatedPolicy()
		defer gate.open()
		gated := serviceWith(h, gate)
		doc := mustCreateDocumentWith(t, h, model.StateArchived)

		// The dispose passes its hold check and parks in its admin check.
		gate.armed.Store(true)
		disposed := make(chan error, 1)
		go func() { disposed <- dispose(gated, h, doc.ID) }()
		awaitParked(t, gate)

		hold, blocked := runConcurrently(t, h, func() error {
			_, herr := compliance.NewHoldsService(h.pool).Create(h.ctx, h.tenant, h.user, compliance.CreateInput{
				Name: "Race matter", DocumentIDs: []uuid.UUID{doc.ID},
			})
			return herr
		})
		require.True(t, blocked, "the hold must not commit while a dispose that already passed its hold check is in flight")
		inband, inbandBlocked := runConcurrently(t, h, func() error {
			_, aerr := h.svc.UpdateLifecycle(h.ctx, &service.UpdateLifecycleInput{
				DocumentID: doc.ID, Action: model.ActionApplyHold, HoldName: "In-band race", Reason: "litigation",
			})
			return aerr
		})
		require.True(t, inbandBlocked, "an in-band apply_hold waits on the same row lock")

		gate.open()
		require.NoError(t, recv(t, disposed), "the dispose was ordered before both holds")
		require.NoError(t, recv(t, hold))
		require.Error(t, recv(t, inband), "apply_hold on a disposed document is not a valid transition")

		got, _, err := h.svc.GetDocument(h.ctx, doc.ID)
		require.NoError(t, err)
		require.Equal(t, model.StateDisposed, got.LifecycleState)
	})
}
