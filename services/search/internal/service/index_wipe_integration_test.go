//go:build integration
// +build integration

// The DoD regression against REAL OpenSearch: index a doc with content +
// ACL → emit a title-only dms.document.updated.v1 → the doc is STILL
// findable by content by an ACL-permitted user (and still invisible to
// everyone else). Under the old wiring the sparse update full-replaced
// the index doc, wiping content + readable_by — the doc vanished from
// every user's results.
//
// Also proves the repair path end-to-end at the indexer: a doc wiped by
// the old bug is restored by the reconcile event
// (dms.document.reindexed.v1 → full replace from source of truth).
//
// Queries run through opensearch.BuildSearchQuery — the same builder the
// service uses — so the readable_by ACL filter semantics are the real
// ones, against a real OpenSearch 2.12.
package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/search/internal/model"
	"github.com/aieera/sedoc/services/search/internal/opensearch"
)

type wipeHarness struct {
	ctx    context.Context
	osc    *opensearch.RealClient
	ix     *Indexer
	osURL  string
	tenant string
}

func newWipeHarness(t *testing.T) *wipeHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	t.Cleanup(cancel)

	osURL, cleanup, err := testutil.NewOpenSearchContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(cleanup)

	osc, err := opensearch.NewReal(ctx, opensearch.Config{URL: osURL, Logger: zerolog.Nop()})
	require.NoError(t, err)
	svc := New(Config{OS: osc, Logger: zerolog.Nop()})
	ix := NewIndexer(svc, nil, nil, zerolog.Nop())

	return &wipeHarness{ctx: ctx, osc: osc, ix: ix, osURL: osURL,
		tenant: uuid.Must(uuid.NewV7()).String()}
}

// refresh forces OpenSearch to make writes visible to search — the tests
// assert on query results, not just document state.
func (h *wipeHarness) refresh(t *testing.T) {
	t.Helper()
	resp, err := http.Post(h.osURL+"/dms-documents-"+h.tenant+"/_refresh", "application/json", nil)
	require.NoError(t, err)
	require.Less(t, resp.StatusCode, 300)
	_ = resp.Body.Close()
}

// searchAs runs a content query through the service's own query builder
// (real ACL filter) and returns the hits.
func (h *wipeHarness) searchAs(t *testing.T, userID, query string) []opensearch.RawHit {
	t.Helper()
	q := opensearch.BuildSearchQuery(&model.SearchRequest{
		TenantID: h.tenant,
		UserID:   userID,
		Query:    query,
	})
	res, err := h.osc.Search(h.ctx, h.tenant, q)
	require.NoError(t, err)
	return res.Hits
}

func TestIntegration_MetadataEditKeepsDocSearchable(t *testing.T) {
	h := newWipeHarness(t)
	docID := uuid.Must(uuid.NewV7()).String()
	const allowedUser = "3aa1a923-0000-4000-8000-000000000001"
	const otherUser = "3aa1a923-0000-4000-8000-000000000002"

	// 1. Doc lands with content + ACL (created event, full payload).
	h.ix.onDocCreated(event(t, h.tenant, map[string]any{
		"document_id":       docID,
		"workspace_id":      uuid.Must(uuid.NewV7()).String(),
		"title":             "Q3 report",
		"content":           "consolidated revenue grew twelve percent",
		"lifecycle_state":   "active",
		"readable_by":       []any{allowedUser},
		"readable_by_users": []any{allowedUser},
	}))
	h.refresh(t)
	require.Len(t, h.searchAs(t, allowedUser, "revenue"), 1, "sanity: ACL user finds doc by content")
	require.Empty(t, h.searchAs(t, otherUser, "revenue"), "sanity: non-ACL user finds nothing")

	// 2. A title-only metadata edit — the event the document service
	//    emits — must NOT knock the doc out of search.
	h.ix.onDocUpdated(event(t, h.tenant, map[string]any{
		"document_id":    docID,
		"changed_fields": []any{"title"},
		"changed":        map[string]any{"title": "Q3 report (final)"},
		"updated_by":     allowedUser,
	}))
	h.refresh(t)

	hits := h.searchAs(t, allowedUser, "revenue")
	require.Len(t, hits, 1,
		"REGRESSION: doc must remain findable by content after a metadata-only edit")
	require.Equal(t, "Q3 report (final)", hits[0].Source["title"], "title change applied")
	require.Empty(t, h.searchAs(t, otherUser, "revenue"), "ACL still enforced")
}

func TestIntegration_ReindexEventRepairsWipedDoc(t *testing.T) {
	h := newWipeHarness(t)
	docID := uuid.Must(uuid.NewV7()).String()
	const allowedUser = "3aa1a923-0000-4000-8000-000000000003"

	// A doc in the state the OLD bug left it: full-replaced from a
	// sparse diff — no content, no readable_by.
	require.NoError(t, h.osc.Index(h.ctx, &model.IndexDocument{
		TenantID:   h.tenant,
		DocumentID: docID,
		Title:      "",
		ReadableBy: []string{},
	}))
	h.refresh(t)
	require.Empty(t, h.searchAs(t, allowedUser, "merger"), "wiped doc is invisible")

	// The reconcile event rebuilt from source of truth repairs it.
	h.ix.onDocCreated(event(t, h.tenant, map[string]any{
		"document_id":       docID,
		"workspace_id":      uuid.Must(uuid.NewV7()).String(),
		"title":             "Acquisition brief",
		"content":           "the merger closes in the fourth quarter",
		"content_snippet":   "the merger closes",
		"lifecycle_state":   "active",
		"readable_by":       []any{allowedUser},
		"readable_by_users": []any{allowedUser},
	}))
	h.refresh(t)

	hits := h.searchAs(t, allowedUser, "merger")
	require.Len(t, hits, 1, "reindexed.v1 full payload must restore searchability")
	require.Equal(t, "Acquisition brief", hits[0].Source["title"])
}
