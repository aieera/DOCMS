//go:build integration

// DEC-7 on batch metadata updates. BatchUpdateMetadata wrote without the
// document row lock, judged legal hold by the lifecycle STATE only (blind to
// a compliance-API hold binding), and skipped the records freeze that
// UpdateDocument enforces — so a declared record's metadata, frozen until
// disposition, could be rewritten in bulk. It now runs UpdateDocument's
// metadata gates per row: LockDocument, assertNotHeld("update_metadata")
// (state AND binding, judged by the DEC-7 allow-list exactly as a single
// update is) and blockedByRecord.
package service_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/document/internal/records"
	"github.com/aieera/sedoc/services/document/internal/service"
)

func TestBulkUpdateMetadata_HeldDocumentRefused(t *testing.T) {
	h := newHarness(t, true)
	rec := records.New(h.pool)
	h.svc.SetRecordsChecker(rec)
	wsID := uuid.Must(uuid.NewV7())
	f := mustCreateFolder(t, h, wsID, "Batch")
	frozen := mustDocIn(t, h, h.svc, wsID, f.ID, "declared record")
	held := mustDocIn(t, h, h.svc, wsID, f.ID, "under compliance hold")
	free := mustDocIn(t, h, h.svc, wsID, f.ID, "free")
	complianceHold(t, h, held.ID)
	cat, err := rec.CreateCategory(h.ctx, h.tenant, records.CategoryInput{Name: "Contracts"})
	require.NoError(t, err)
	_, err = rec.Declare(h.ctx, h.tenant, h.user, records.DeclareInput{DocumentID: frozen.ID, CategoryID: cat.ID})
	require.NoError(t, err)

	update := map[string]any{"matter": "rewritten"}
	res, err := h.svc.BatchUpdateMetadata(h.ctx, &service.BatchUpdateMetadataInput{
		DocumentIDs: []uuid.UUID{frozen.ID, held.ID, free.ID}, MetadataUpdates: update,
	})
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{frozen.ID}, res.FailedDocumentIDs, "a declared record's metadata is frozen")
	require.Equal(t, []string{"record_declared"}, res.FailureReasons)
	require.Equal(t, 2, res.UpdatedCount)
	require.Zero(t, countRows(t, h,
		`SELECT count(*) FROM documents WHERE tenant_id = $1 AND id = $2 AND custom_metadata ? 'matter'`, h.tenant, frozen.ID))

	// A legal hold is judged by the same allow-list as a single update:
	// the batch result for the held document matches UpdateDocument's.
	_, single := h.svc.UpdateDocument(h.ctx, &service.UpdateDocumentInput{
		DocumentID: held.ID, CustomMetadata: update, UpdatedBy: h.user,
	})
	require.NoError(t, single, "DEC-7 keeps custom_metadata editable under hold")
	require.Equal(t, 1, countRows(t, h,
		`SELECT count(*) FROM documents WHERE tenant_id = $1 AND id = $2 AND custom_metadata->>'matter' = 'rewritten'`, h.tenant, held.ID))
}
