//go:build integration

// Track 1 (handoff audit-2026-05): the trash backend must keep restore aligned
// with the retention pipeline — a document whose retention window has elapsed
// is eligible for disposal and must NOT be restorable (409), while one still
// under retention restores normally. retention_exempt short-circuits the check.
package service_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/services/document/internal/model"
)

func setRetention(t *testing.T, h *harness, id uuid.UUID, until time.Time, exempt bool) {
	t.Helper()
	require.NoError(t, database.WithTenantTx(h.ctx, h.repos.Pool, h.tenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(h.ctx,
			`UPDATE documents SET retention_until = $3, retention_exempt = $4 WHERE tenant_id = $1 AND id = $2`,
			h.tenant, id, until, exempt)
		return e
	}))
}

// softDelete puts a fresh document into the soft-deleted state by setting
// deleted_at directly. We bypass svc.DeleteDocument deliberately: its cascade
// touches intelligence tables not present in the document-only testcontainer,
// and these tests exercise the restore retention guard, not the delete path.
func softDelete(t *testing.T, h *harness) *model.Document {
	t.Helper()
	doc := mustCreateDocumentWith(t, h, model.StateDraft)
	require.NoError(t, database.WithTenantTx(h.ctx, h.repos.Pool, h.tenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(h.ctx,
			`UPDATE documents SET deleted_at = now() WHERE tenant_id = $1 AND id = $2`,
			h.tenant, doc.ID)
		return e
	}))
	return doc
}

func TestRestoreDocument_BlockedByElapsedRetention(t *testing.T) {
	h := newHarness(t, true)
	doc := softDelete(t, h)
	setRetention(t, h, doc.ID, time.Now().Add(-48*time.Hour), false) // retention elapsed
	err := h.svc.RestoreDocument(h.ctx, doc.ID)
	require.Error(t, err)
	require.ErrorContains(t, err, "retention")
}

func TestRestoreDocument_AllowedUnderRetention(t *testing.T) {
	h := newHarness(t, true)
	doc := softDelete(t, h)
	setRetention(t, h, doc.ID, time.Now().Add(48*time.Hour), false) // still under retention
	require.NoError(t, h.svc.RestoreDocument(h.ctx, doc.ID))
}

func TestRestoreDocument_ExemptIgnoresElapsedRetention(t *testing.T) {
	h := newHarness(t, true)
	doc := softDelete(t, h)
	setRetention(t, h, doc.ID, time.Now().Add(-48*time.Hour), true) // elapsed BUT exempt → restorable
	require.NoError(t, h.svc.RestoreDocument(h.ctx, doc.ID))
}
