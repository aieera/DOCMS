//go:build integration
// +build integration

// Emitter + reconcile side of the search-wipe fix:
//
//   - UpdateDocument must ship the NEW VALUES of changed fields on
//     dms.document.updated.v1 (payload.changed) — the search indexer
//     applies them as a partial update. Name-only diffs are what forced
//     the old full-replace path that wiped content + readable_by.
//   - ReindexSearch must rebuild the FULL search projection from source
//     of truth (documents row + ocr_results text + folder ACL) and emit
//     it as dms.document.reindexed.v1 through the outbox — the repair
//     path for already-wiped docs.
package service_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/document/internal/service"
)

// outboxPayload returns the newest outbox payload for (event_type,
// aggregate_id) parsed into a map.
func outboxPayload(t *testing.T, h *harness, eventType string, aggregateID uuid.UUID) map[string]any {
	t.Helper()
	var raw []byte
	require.NoError(t, h.pool.QueryRow(h.ctx, `
		SELECT payload FROM outbox
		WHERE tenant_id = $1 AND event_type = $2 AND aggregate_id = $3
		ORDER BY created_at DESC LIMIT 1`,
		h.tenant, eventType, aggregateID).Scan(&raw))
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func TestUpdateDocument_EmitsChangedValues(t *testing.T) {
	h := newHarness(t, true)
	wsID := uuid.Must(uuid.NewV7())
	folder := mustCreateFolder(t, h, wsID, "Root")
	doc, err := h.svc.CreateDocument(h.ctx, &service.CreateDocumentInput{
		UpdatedBy: h.user, WorkspaceID: wsID, FolderID: folder.ID,
		Title: "Old title", RegionPin: "us-east-1",
	})
	require.NoError(t, err)

	newTitle := "New title after metadata edit"
	_, err = h.svc.UpdateDocument(h.ctx, &service.UpdateDocumentInput{
		DocumentID: doc.ID,
		UpdatedBy:  h.user,
		Title:      &newTitle,
		Tags:       []string{"q3", "finance"},
	})
	require.NoError(t, err)

	payload := outboxPayload(t, h, "dms.document.updated.v1", doc.ID)
	require.ElementsMatch(t, []any{"title", "tags"}, payload["changed_fields"])
	changed, ok := payload["changed"].(map[string]any)
	require.True(t, ok, "updated.v1 must carry the changed VALUES, not just names")
	require.Equal(t, newTitle, changed["title"])
	require.ElementsMatch(t, []any{"q3", "finance"}, changed["tags"])
	// The diff shape must stay sparse — no pipeline-owned fields.
	require.NotContains(t, changed, "content")
	require.NotContains(t, changed, "readable_by")
}

func TestReindexSearch_EmitsFullProjectionFromSourceOfTruth(t *testing.T) {
	h := newHarness(t, true)
	wsID := uuid.Must(uuid.NewV7())
	folder := mustCreateFolder(t, h, wsID, "Root")
	doc, err := h.svc.CreateDocument(h.ctx, &service.CreateDocumentInput{
		UpdatedBy: h.user, WorkspaceID: wsID, FolderID: folder.ID,
		Title: "Contract pack", RegionPin: "us-east-1", Tags: []string{"legal"},
	})
	require.NoError(t, err)

	// The acting user is a workspace member → must land in readable_by.
	_, err = h.pool.Exec(h.ctx, `
		INSERT INTO workspace_members (tenant_id, workspace_id, user_id, role)
		VALUES ($1, $2, $3, 'member') ON CONFLICT DO NOTHING`,
		h.tenant, wsID, h.user)
	require.NoError(t, err)

	// Current version + its OCR text (what the intelligence worker
	// persists to ocr_results and the index calls `content`).
	blobID := uuid.New()
	_, err = h.pool.Exec(h.ctx, `
		INSERT INTO content_blobs (id, tenant_id, sha256_hash, storage_bucket, storage_key, size_bytes, mime_type)
		VALUES ($1, $2, $3, 'b', 'k', 42, 'application/pdf')`,
		blobID, h.tenant, uuid.NewString())
	require.NoError(t, err)
	v, err := h.svc.CreateVersion(h.ctx, &service.CreateVersionInput{
		DocumentID: doc.ID, ContentBlobID: blobID,
		SizeBytes: 42, MimeType: "application/pdf", SHA256Hash: "deadbeef",
	})
	require.NoError(t, err)
	for i, text := range []string{"page one: indemnification clause", "page two: governing law"} {
		_, err = h.pool.Exec(h.ctx, `
			INSERT INTO ocr_results (tenant_id, version_id, page_number, text_content, confidence)
			VALUES ($1, $2, $3, $4, 0.99)`,
			h.tenant, v.ID, i+1, text)
		require.NoError(t, err)
	}

	n, err := h.svc.ReindexSearch(h.ctx, &doc.ID)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	payload := outboxPayload(t, h, "dms.document.reindexed.v1", doc.ID)
	require.Equal(t, "Contract pack", payload["title"])
	require.ElementsMatch(t, []any{"legal"}, payload["tags"])
	require.Equal(t, "page one: indemnification clause\npage two: governing law", payload["content"],
		"content must be rebuilt from ocr_results pages in order")
	require.Contains(t, payload["readable_by"], h.user.String(),
		"readable_by must be recomputed from the folder/workspace ACL")
	require.Contains(t, payload["readable_by_users"], h.user.String())
	require.Equal(t, float64(1), payload["version_count"])
	require.Equal(t, "application/pdf", payload["mime_type"])

	// Whole-tenant sweep finds it too (and terminates).
	total, err := h.svc.ReindexSearch(h.ctx, nil)
	require.NoError(t, err)
	require.GreaterOrEqual(t, total, 1)
}

func TestReindexSearch_UnknownDocIs404(t *testing.T) {
	h := newHarness(t, true)
	ghost := uuid.Must(uuid.NewV7())
	_, err := h.svc.ReindexSearch(h.ctx, &ghost)
	require.Error(t, err)
}
