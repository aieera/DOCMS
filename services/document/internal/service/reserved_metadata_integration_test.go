//go:build integration

// D-25 (review round 1): the orphan GC exempted documents by a metadata key
// (email.source) that any user can write, so anyone could shield an
// abandoned upload from the sweep. System markers now live in the reserved
// `_sedoc.` namespace: every user-facing metadata write refuses those keys,
// only server code sets them (CreateDocumentInput.SystemMetadata), and a
// user metadata replace keeps them.
package service_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/service"
)

func TestMetadataWrite_RejectsReservedKeys(t *testing.T) {
	h := newHarness(t, true)
	wsID := uuid.Must(uuid.NewV7())
	f := mustCreateFolder(t, h, wsID, "Reserved")
	doc := mustDocIn(t, h, h.svc, wsID, f.ID, "plain")
	spoof := map[string]any{"_sedoc.source": "m365", "matter": "x"}
	isValidation := func(err error, what string) {
		t.Helper()
		require.Error(t, err, what)
		require.Equal(t, vdmserr.KindValidation, vdmserr.KindOf(err), "%s: %v", what, err)
		require.ErrorContains(t, err, "_sedoc.", what)
	}

	_, err := h.svc.CreateDocument(h.ctx, &service.CreateDocumentInput{
		WorkspaceID: wsID, FolderID: f.ID, Title: "spoofed", CustomMetadata: spoof, UpdatedBy: h.user,
	})
	isValidation(err, "CreateDocument")

	_, err = h.svc.UpdateDocument(h.ctx, &service.UpdateDocumentInput{
		DocumentID: doc.ID, CustomMetadata: spoof, UpdatedBy: h.user,
	})
	isValidation(err, "UpdateDocument")

	_, err = h.svc.BatchUpdateMetadata(h.ctx, &service.BatchUpdateMetadataInput{
		DocumentIDs: []uuid.UUID{doc.ID}, MetadataUpdates: spoof,
	})
	isValidation(err, "BatchUpdateMetadata")

	_, err = h.svc.UpsertDocumentByExternalKey(h.ctx, &service.UpsertByExternalKeyInput{
		ExternalID: "ERP-1", WorkspaceID: wsID, FolderID: f.ID, CustomMetadata: spoof,
		BlobChecksum: "00" + uuid.NewString(), UpdatedBy: h.user,
	})
	isValidation(err, "UpsertDocumentByExternalKey")

	require.Zero(t, countRows(t, h,
		`SELECT count(*) FROM documents WHERE tenant_id = $1 AND custom_metadata ? '_sedoc.source'`, h.tenant))

	// Server code sets the marker; a later user replace keeps it.
	sys, err := h.svc.CreateDocument(h.ctx, &service.CreateDocumentInput{
		WorkspaceID: wsID, FolderID: f.ID, Title: "ingested", UpdatedBy: h.user,
		SystemMetadata: map[string]any{service.SystemSourceKey: service.SystemSourceM365},
	})
	require.NoError(t, err)
	_, err = h.svc.UpdateDocument(h.ctx, &service.UpdateDocumentInput{
		DocumentID: sys.ID, CustomMetadata: map[string]any{"matter": "y"}, UpdatedBy: h.user,
	})
	require.NoError(t, err)
	require.Equal(t, 1, countRows(t, h,
		`SELECT count(*) FROM documents WHERE tenant_id = $1 AND id = $2
		   AND custom_metadata->>'_sedoc.source' = 'm365' AND custom_metadata->>'matter' = 'y'`, h.tenant, sys.ID))
}
