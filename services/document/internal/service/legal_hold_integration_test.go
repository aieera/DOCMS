//go:build integration

// H-8 / DEC-7: a legal hold placed through the compliance API binds the
// document in legal_hold_documents without touching lifecycle_state. Every
// mutating path must honour that binding, not just the in-band legal_hold
// state; and the in-band apply_hold / release_hold actions must write and
// release real rows in that same schema.
package service_test

import (
	"testing"

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
