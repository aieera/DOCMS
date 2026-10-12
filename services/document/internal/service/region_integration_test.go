//go:build integration

// M-7 / DEC-11: region_pin must not be a label that can lie. A new document
// inherits its workspace's pin (else the tenant's, else SEDOC_REGION) rather
// than a hard-coded us-east-1 or a caller-chosen value, and a version may
// only reference bytes stored in the document's region.
package service_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// regionFolder seeds a workspace pinned to region (may be "") with a root
// folder and returns both ids.
func regionFolder(t *testing.T, h *harness, region string) (uuid.UUID, uuid.UUID) {
	t.Helper()
	wsID := uuid.Must(uuid.NewV7())
	_, err := h.pool.Exec(h.ctx, `
		INSERT INTO workspaces (tenant_id, id, name, region_pin, created_by)
		VALUES ($1, $2, 'ws', $3, $4)`, h.tenant, wsID, region, h.user)
	require.NoError(t, err)
	f, err := h.svc.CreateFolder(h.ctx, &service.CreateFolderInput{WorkspaceID: wsID, Name: "Root"})
	require.NoError(t, err)
	return wsID, f.ID
}

func createIn(t *testing.T, h *harness, wsID, folderID uuid.UUID, region string) *model.Document {
	t.Helper()
	doc, err := h.svc.CreateDocument(h.ctx, &service.CreateDocumentInput{
		UpdatedBy: h.user, WorkspaceID: wsID, FolderID: folderID, Title: "pinned", RegionPin: region,
	})
	require.NoError(t, err)
	return doc
}

func TestCreateDocument_InheritsWorkspaceRegion(t *testing.T) {
	h := newHarness(t, true)

	euWS, euFolder := regionFolder(t, h, "eu-west-1")
	require.Equal(t, "eu-west-1", createIn(t, h, euWS, euFolder, "").RegionPin)
	require.Equal(t, "eu-west-1", createIn(t, h, euWS, euFolder, "us-east-1").RegionPin,
		"a caller-supplied region does not override the workspace's residency")

	// No workspace pin → the tenant's primary region (me-central-1 is a
	// supported region now).
	_, err := h.pool.Exec(h.ctx, `UPDATE organizations SET primary_region = 'me-central-1' WHERE id = $1`, h.tenant)
	require.NoError(t, err)
	bareWS, bareFolder := regionFolder(t, h, "")
	require.Equal(t, "me-central-1", createIn(t, h, bareWS, bareFolder, "me-central-1").RegionPin)

	// Neither → the deployment region.
	_, err = h.pool.Exec(h.ctx, `UPDATE organizations SET primary_region = '' WHERE id = $1`, h.tenant)
	require.NoError(t, err)
	t.Setenv("SEDOC_REGION", "ap-southeast-1")
	require.Equal(t, "ap-southeast-1", createIn(t, h, bareWS, bareFolder, "").RegionPin)

	_, err = h.svc.CreateDocument(h.ctx, &service.CreateDocumentInput{
		UpdatedBy: h.user, WorkspaceID: euWS, FolderID: euFolder, Title: "x", RegionPin: "mars-north-1",
	})
	require.Error(t, err, "unsupported regions are still rejected")
}

func TestCreateVersion_RegionMismatchRejected(t *testing.T) {
	h := newHarness(t, true)
	euWS, euFolder := regionFolder(t, h, "eu-west-1")
	doc := createIn(t, h, euWS, euFolder, "")

	usBlob := seedUploadedBlob(t, h, h.user, "us-east-1")
	_, err := h.svc.CreateVersion(h.ctx, &service.CreateVersionInput{DocumentID: doc.ID, ContentBlobID: usBlob.ID})
	require.ErrorIs(t, err, vdmserr.ErrRegionViolation)
	require.Zero(t, countRows(t, h,
		`SELECT count(*) FROM document_versions WHERE tenant_id = $1 AND document_id = $2`, h.tenant, doc.ID))

	euBlob := seedUploadedBlob(t, h, h.user, "eu-west-1")
	_, err = h.svc.CreateVersion(h.ctx, &service.CreateVersionInput{DocumentID: doc.ID, ContentBlobID: euBlob.ID})
	require.NoError(t, err)

	// The ERP append path and copy obey the same rule.
	_, err = h.svc.UpsertDocumentByExternalKey(h.ctx, &service.UpsertByExternalKeyInput{
		ExternalID: "EU-1", WorkspaceID: euWS, FolderID: euFolder,
		BlobChecksum: seedUploadedBlob(t, h, h.user, "us-east-1").SHA, UpdatedBy: h.user,
	})
	require.ErrorIs(t, err, vdmserr.ErrRegionViolation)
	usWS, usFolder := regionFolder(t, h, "us-east-1")
	_, err = h.svc.CopyDocument(h.ctx, &service.CopyDocumentInput{
		DocumentID: doc.ID, TargetFolderID: usFolder, TargetWorkspaceID: &usWS, CopiedBy: h.user,
	})
	require.ErrorIs(t, err, vdmserr.ErrRegionViolation, "a cross-region copy needs a residency migration")
}
