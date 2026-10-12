//go:build integration

// M-1 / D-1 / DEC-12: a caller-supplied blob id or sha256 is linkable only
// when the caller completed the upload session that produced the blob, or
// can view a live document that already carries it. Otherwise CreateVersion
// answers NotFound and the ERP upsert answers the same "no uploaded content
// blob" error as for bytes that were never uploaded — neither an escalation
// to read someone else's bytes nor an existence oracle.
package service_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/service"
)

func TestCreateVersion_ForeignBlobNotFound(t *testing.T) {
	h := newHarness(t, true)
	other := seedOtherUser(t, h)
	otherCtx := auth.WithUser(h.ctx, auth.UserInfo{ID: other, TenantID: h.tenant, Role: "member"})
	wsID := uuid.Must(uuid.NewV7())
	f := mustCreateFolder(t, h, wsID, "Claims")
	mine := mustDocIn(t, h, h.svc, wsID, f.ID, "mine")

	// 1. Bytes another member uploaded and nobody has linked yet.
	unlinked := seedUploadedBlob(t, h, other, "us-east-1")
	_, err := h.svc.CreateVersion(h.ctx, &service.CreateVersionInput{DocumentID: mine.ID, ContentBlobID: unlinked.ID})
	require.ErrorIs(t, err, vdmserr.ErrNotFound, "an unlinked foreign blob must not be claimable")

	// 2. Bytes that back a document the caller cannot view.
	private := mustDocIn(t, h, h.svc, wsID, f.ID, "private")
	held := seedUploadedBlob(t, h, other, "us-east-1")
	_, err = h.svc.CreateVersion(otherCtx, &service.CreateVersionInput{DocumentID: private.ID, ContentBlobID: held.ID})
	require.NoError(t, err, "the uploader may link their own blob")
	blind := serviceWith(h, denyPolicy{deny: map[string]bool{private.ID.String(): true}})
	_, err = blind.CreateVersion(h.ctx, &service.CreateVersionInput{DocumentID: mine.ID, ContentBlobID: held.ID})
	require.ErrorIs(t, err, vdmserr.ErrNotFound, "a blob behind an unviewable document must not be claimable")

	// 3. A blob id that does not exist looks exactly the same.
	_, err = h.svc.CreateVersion(h.ctx, &service.CreateVersionInput{DocumentID: mine.ID, ContentBlobID: uuid.New()})
	require.ErrorIs(t, err, vdmserr.ErrNotFound)
	require.Zero(t, countRows(t, h,
		`SELECT count(*) FROM document_versions WHERE tenant_id = $1 AND document_id = $2`, h.tenant, mine.ID))

	// Controls: viewing a document that carries the blob is a claim, and so
	// is having uploaded it.
	v, err := h.svc.CreateVersion(h.ctx, &service.CreateVersionInput{DocumentID: mine.ID, ContentBlobID: held.ID})
	require.NoError(t, err)
	require.Equal(t, held.SHA, v.SHA256Hash)
	own := seedUploadedBlob(t, h, h.user, "us-east-1")
	_, err = h.svc.CreateVersion(h.ctx, &service.CreateVersionInput{DocumentID: mine.ID, ContentBlobID: own.ID})
	require.NoError(t, err)
}

func TestUpsertByExternalKey_ForeignChecksumRejected(t *testing.T) {
	h := newHarness(t, true)
	other := seedOtherUser(t, h)
	wsID := uuid.Must(uuid.NewV7())
	f := mustCreateFolder(t, h, wsID, "ERP")
	foreign := seedUploadedBlob(t, h, other, "us-east-1")

	upsert := func(sha, ref string) (*service.UpsertByExternalKeyResult, error) {
		return h.svc.UpsertDocumentByExternalKey(h.ctx, &service.UpsertByExternalKeyInput{
			ExternalID: "ERP-CLAIM-1", WorkspaceID: wsID, FolderID: f.ID,
			BlobChecksum: sha, BlobRef: ref, UpdatedBy: h.user,
		})
	}

	_, err := upsert(foreign.SHA, "")
	require.Error(t, err)
	require.ErrorContains(t, err, "no uploaded content blob matches this checksum")
	_, err = upsert(foreign.SHA, foreign.ID.String())
	require.Error(t, err, "naming the blob id does not make it claimable")
	require.ErrorContains(t, err, "no uploaded content blob matches this checksum")
	_, err = upsert("00"+foreign.SHA[2:], "")
	require.ErrorContains(t, err, "no uploaded content blob matches this checksum", "same answer as unknown bytes")
	require.Zero(t, countRows(t, h,
		`SELECT count(*) FROM documents WHERE tenant_id = $1 AND external_id = 'ERP-CLAIM-1'`, h.tenant))

	// The caller's own upload is accepted.
	own := seedUploadedBlob(t, h, h.user, "us-east-1")
	res, err := upsert(own.SHA, "")
	require.NoError(t, err)
	require.True(t, res.Created)
	require.Equal(t, 1, countRows(t, h,
		`SELECT count(*) FROM document_versions WHERE tenant_id = $1 AND document_id = $2 AND content_blob_id = $3`,
		h.tenant, res.DocumentID, own.ID))
}
