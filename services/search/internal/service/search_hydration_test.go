// Regression tests for the dense-vector (semantic / hybrid) result path.
//
// The bug these pin down: the vector branch produced hits carrying ONLY a
// document_id. Qdrant returns ids and scores, and nothing hydrated them
// against OpenSearch, so semantic mode returned N rows with title:"",
// size_bytes:0, created_at absent, lifecycle_state:"" — the UI rendered
// them as dead "Untitled document" cards. Hybrid inherited the broken half.
// Three consequences are covered here:
//
//   - hydration: vector rows carry the SAME metadata lexical rows do;
//   - filtering: the request's own filters apply to the vector branch, and
//     a candidate that cannot be hydrated is dropped from the results AND
//     from total_count (never returned blank, never counted);
//   - facets: buckets describe the rows actually returned, so they no
//     longer contradict the reported total.
//
// The fakes emulate just enough of OpenSearch / the embedding service /
// Qdrant to exercise Service.Search end to end through the real HTTP
// clients — the layer where the stubbing happened is the layer the bug
// lived in.
package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/search/internal/model"
	"github.com/aieera/sedoc/services/search/internal/opensearch"
	"github.com/aieera/sedoc/services/search/internal/vector"
)

// ---- fakes ----------------------------------------------------------------

// osDoc is a compact way to declare an OpenSearch _source in a test.
type osDoc struct {
	ID        string
	Title     string
	Lifecycle string
	Author    string
	SizeBytes int64
	CreatedAt string
}

func (d osDoc) source() map[string]any {
	src := map[string]any{
		"document_id":     d.ID,
		"title":           d.Title,
		"lifecycle_state": d.Lifecycle,
		"created_by_name": d.Author,
		"size_bytes":      d.SizeBytes,
		"content_snippet": "snippet for " + d.ID,
		"workspace_id":    "ws-1",
	}
	if d.CreatedAt != "" {
		src["created_at"] = d.CreatedAt
	}
	return src
}

func osHits(total int64, docs []osDoc) map[string]any {
	hits := make([]any, 0, len(docs))
	for _, d := range docs {
		hits = append(hits, map[string]any{
			"_id": d.ID, "_score": 1.0, "_source": d.source(),
		})
	}
	return map[string]any{
		"hits": map[string]any{
			"total": map[string]any{"value": total},
			"hits":  hits,
		},
	}
}

// fakeOS answers _search. It distinguishes the main (lexical) query from
// the by-ids hydration query by looking for the `ids` clause, and records
// every body so a test can assert what was actually sent.
type fakeOS struct {
	mu     sync.Mutex
	bodies []map[string]any

	lexTotal int64
	lexDocs  []osDoc
	lexAggs  map[string]any

	// hydrateDocs are the documents OpenSearch is willing to return for a
	// by-ids query. An id absent from here models "deleted, revoked, or
	// filtered out".
	hydrateDocs []osDoc
}

func (f *fakeOS) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/_index_template/") {
			w.WriteHeader(http.StatusOK)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if ids, ok := idsFromBody(body); ok {
			want := map[string]bool{}
			for _, id := range ids {
				want[id] = true
			}
			var out []osDoc
			for _, d := range f.hydrateDocs {
				if want[d.ID] {
					out = append(out, d)
				}
			}
			_ = json.NewEncoder(w).Encode(osHits(int64(len(out)), out))
			return
		}
		resp := osHits(f.lexTotal, f.lexDocs)
		if f.lexAggs != nil {
			resp["aggregations"] = f.lexAggs
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
}

// bodyFor returns the first recorded _search body that does (or does not)
// carry an `ids` clause.
func (f *fakeOS) bodyFor(t *testing.T, hydration bool) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range f.bodies {
		if _, ok := idsFromBody(b); ok == hydration {
			return b
		}
	}
	t.Fatalf("no recorded _search body with hydration=%v (recorded %d)", hydration, len(f.bodies))
	return nil
}

// idsFromBody digs the `ids.values` list out of a bool.filter chain.
func idsFromBody(body map[string]any) ([]string, bool) {
	q, _ := body["query"].(map[string]any)
	b, _ := q["bool"].(map[string]any)
	filters, _ := b["filter"].([]any)
	for _, f := range filters {
		fm, _ := f.(map[string]any)
		idsClause, ok := fm["ids"].(map[string]any)
		if !ok {
			continue
		}
		vals, _ := idsClause["values"].([]any)
		out := make([]string, 0, len(vals))
		for _, v := range vals {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out, true
	}
	return nil, false
}

// newHarness wires a Service whose OpenSearch, embedding and Qdrant
// clients all point at in-process fakes. semanticIDs is what Qdrant
// "finds", in rank order.
func newHarness(t *testing.T, f *fakeOS, semanticIDs []string) *Service {
	t.Helper()

	osSrv := httptest.NewServer(f.handler())
	t.Cleanup(osSrv.Close)
	osc, err := opensearch.NewReal(t.Context(), opensearch.Config{URL: osSrv.URL, Logger: zerolog.Nop()})
	require.NoError(t, err)

	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"embedding":[0.1,0.2,0.3],"dimension":3,"model":"test"}`))
	}))
	t.Cleanup(embedSrv.Close)

	qdrantSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		result := make([]any, 0, len(semanticIDs))
		for i, id := range semanticIDs {
			result = append(result, map[string]any{
				"score":   1.0 - float64(i)/100,
				"payload": map[string]any{"document_id": id},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result})
	}))
	t.Cleanup(qdrantSrv.Close)

	vec, err := vector.New(vector.Config{
		IntelligenceEmbedURL: embedSrv.URL,
		QdrantBaseURL:        qdrantSrv.URL,
		Collection:           "test_chunks",
	})
	require.NoError(t, err)

	return New(Config{OS: osc, Vector: vec, Logger: zerolog.Nop()})
}

func searchReq(mode string) *model.SearchRequest {
	return &model.SearchRequest{
		TenantID: "11111111-1111-1111-1111-111111111111",
		UserID:   "alice",
		GroupIDs: []string{"engineering"},
		Query:    "invoice",
		Mode:     mode,
	}
}

// ---- tests ----------------------------------------------------------------

// TestSearch_SemanticResultsAreHydrated is the BUG-02 regression: every
// row from the vector branch must carry the same metadata a lexical row
// does. Before the fix each hit was {document_id} and nothing else.
func TestSearch_SemanticResultsAreHydrated(t *testing.T) {
	f := &fakeOS{
		lexTotal: 1,
		lexDocs:  []osDoc{{ID: "doc-lex", Title: "Lexical only", Lifecycle: "active", Author: "Bob", SizeBytes: 10}},
		hydrateDocs: []osDoc{
			{ID: "doc-a", Title: "Invoice 2026-04", Lifecycle: "active", Author: "Alice", SizeBytes: 4096, CreatedAt: "2026-04-01T09:00:00Z"},
			{ID: "doc-b", Title: "Invoice 2026-05", Lifecycle: "archived", Author: "Bob", SizeBytes: 8192, CreatedAt: "2026-05-01T09:00:00Z"},
		},
	}
	svc := newHarness(t, f, []string{"doc-a", "doc-b"})

	res, err := svc.Search(t.Context(), searchReq(model.SearchModeSemantic))
	require.NoError(t, err)

	require.Len(t, res.Results, 2)
	require.EqualValues(t, 2, res.TotalCount)
	for _, hit := range res.Results {
		require.NotEmpty(t, hit.DocumentID)
		require.NotEmpty(t, hit.Title, "semantic hit %s came back with a blank title", hit.DocumentID)
		require.NotEmpty(t, hit.LifecycleState, "lifecycle_state feeds the facet sidebar")
		require.NotEmpty(t, hit.CreatedByName, "created_by_name feeds the author facet")
		require.NotZero(t, hit.SizeBytes)
		require.NotNil(t, hit.CreatedAt, "created_at must be hydrated, not absent")
		require.False(t, hit.CreatedAt.IsZero(), "created_at must not be the Go zero time")
		require.NotEmpty(t, hit.ContentSnippet)
	}
}

// TestSearch_UnhydratableSemanticHitsAreDropped is the BUG-03 half about
// ghost rows: a vector candidate OpenSearch will not return (deleted,
// permission-revoked, or filtered out) must vanish from BOTH the results
// and total_count — not be returned as a blank row and counted.
func TestSearch_UnhydratableSemanticHitsAreDropped(t *testing.T) {
	f := &fakeOS{
		lexTotal: 0,
		hydrateDocs: []osDoc{
			{ID: "doc-a", Title: "Invoice 2026-04", Lifecycle: "active", Author: "Alice", SizeBytes: 4096, CreatedAt: "2026-04-01T09:00:00Z"},
		},
	}
	// Qdrant offers three candidates; only doc-a survives OpenSearch.
	svc := newHarness(t, f, []string{"doc-a", "doc-deleted", "doc-revoked"})

	res, err := svc.Search(t.Context(), searchReq(model.SearchModeSemantic))
	require.NoError(t, err)

	require.Len(t, res.Results, 1, "unhydratable candidates must not be returned")
	require.EqualValues(t, 1, res.TotalCount, "total_count must not count candidates that were dropped")
	require.Equal(t, "doc-a", res.Results[0].DocumentID)
}

// TestSearch_FiltersApplyToVectorBranch is the core of BUG-03: the
// request's filters must be part of the hydration query, and when they
// exclude everything the answer is zero results AND zero total — not the
// pre-filter candidate count.
func TestSearch_FiltersApplyToVectorBranch(t *testing.T) {
	f := &fakeOS{lexTotal: 0} // no hydrateDocs: OpenSearch filters everything out
	svc := newHarness(t, f, []string{"doc-a", "doc-b", "doc-c", "doc-d", "doc-e"})

	after := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	before := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	req := searchReq(model.SearchModeHybrid)
	req.Filters.CreatedAfter = &after
	req.Filters.CreatedBefore = &before

	res, err := svc.Search(t.Context(), req)
	require.NoError(t, err)

	require.Empty(t, res.Results, "an impossible date range must return no rows")
	require.EqualValues(t, 0, res.TotalCount, "total_count must reflect the filtered set")

	// The hydration query must actually carry the caller's filters —
	// otherwise the vector branch is filtering by luck.
	body, err := json.Marshal(f.bodyFor(t, true))
	require.NoError(t, err)
	require.Contains(t, string(body), `"created_at"`,
		"hydration query must carry the request's date range")
	require.Contains(t, string(body), "2026-12-31", "created_after bound missing from hydration query")
	require.Contains(t, string(body), "2020-01-01", "created_before bound missing from hydration query")
	require.Contains(t, string(body), `"readable_by_users"`,
		"hydration query must keep the ACL re-check (Epic 9 #3)")
}

// TestSearch_HybridFusesHydratedRowsAndCountsOnlyThem covers the fused
// shape end to end: lexical rows keep their source, vector-only rows get
// hydrated, a ghost candidate is dropped, and total_count is the lexical
// total plus the vector-only survivors.
func TestSearch_HybridFusesHydratedRowsAndCountsOnlyThem(t *testing.T) {
	lex := []osDoc{
		{ID: "doc-1", Title: "Invoice A", Lifecycle: "active", Author: "Alice", SizeBytes: 100, CreatedAt: "2026-01-01T00:00:00Z"},
		{ID: "doc-2", Title: "Invoice B", Lifecycle: "active", Author: "Alice", SizeBytes: 200, CreatedAt: "2026-01-02T00:00:00Z"},
		{ID: "doc-3", Title: "Invoice C", Lifecycle: "active", Author: "Bob", SizeBytes: 300, CreatedAt: "2026-01-03T00:00:00Z"},
	}
	f := &fakeOS{
		lexTotal: 3,
		lexDocs:  lex,
		hydrateDocs: []osDoc{
			{ID: "doc-2", Title: "Invoice B", Lifecycle: "active", Author: "Alice", SizeBytes: 200, CreatedAt: "2026-01-02T00:00:00Z"},
			{ID: "doc-4", Title: "Payment advice", Lifecycle: "draft", Author: "Carol", SizeBytes: 400, CreatedAt: "2026-02-01T00:00:00Z"},
			{ID: "doc-5", Title: "Remittance", Lifecycle: "draft", Author: "Carol", SizeBytes: 500, CreatedAt: "2026-02-02T00:00:00Z"},
		},
	}
	// doc-ghost is a stale Qdrant payload with no live OpenSearch document.
	svc := newHarness(t, f, []string{"doc-2", "doc-4", "doc-5", "doc-ghost"})

	res, err := svc.Search(t.Context(), searchReq(model.SearchModeHybrid))
	require.NoError(t, err)

	ids := make([]string, 0, len(res.Results))
	for _, h := range res.Results {
		ids = append(ids, h.DocumentID)
		require.NotEmpty(t, h.Title, "fused hit %s came back blank", h.DocumentID)
		require.NotNil(t, h.CreatedAt)
	}
	require.ElementsMatch(t, []string{"doc-1", "doc-2", "doc-3", "doc-4", "doc-5"}, ids)
	require.NotContains(t, ids, "doc-ghost", "a candidate with no live document must be dropped")
	// 3 lexical matches + the 2 documents only the vector branch found.
	require.EqualValues(t, 5, res.TotalCount)
}

// TestSearch_FacetsDescribeTheFusedResultSet is BUG-05's count half: the
// OpenSearch aggregations only see the BM25 match set, so the sidebar
// reported 3 while the header reported the fused total. Derived facets
// must add up to the rows actually returned.
func TestSearch_FacetsDescribeTheFusedResultSet(t *testing.T) {
	f := &fakeOS{
		lexTotal: 3,
		lexDocs: []osDoc{
			{ID: "doc-1", Title: "Invoice A", Lifecycle: "active", Author: "Alice", SizeBytes: 100, CreatedAt: "2026-01-01T00:00:00Z"},
			{ID: "doc-2", Title: "Invoice B", Lifecycle: "active", Author: "Alice", SizeBytes: 200, CreatedAt: "2026-01-02T00:00:00Z"},
			{ID: "doc-3", Title: "Invoice C", Lifecycle: "active", Author: "Bob", SizeBytes: 300, CreatedAt: "2026-01-03T00:00:00Z"},
		},
		// What OpenSearch would have said about the lexical half alone.
		lexAggs: map[string]any{
			"lifecycle_state": map[string]any{"buckets": []any{
				map[string]any{"key": "active", "doc_count": 3.0},
			}},
		},
		hydrateDocs: []osDoc{
			{ID: "doc-4", Title: "Payment advice", Lifecycle: "draft", Author: "Carol", SizeBytes: 400, CreatedAt: "2026-02-01T00:00:00Z"},
			{ID: "doc-5", Title: "Remittance", Lifecycle: "draft", Author: "Carol", SizeBytes: 500, CreatedAt: "2026-02-02T00:00:00Z"},
		},
	}
	svc := newHarness(t, f, []string{"doc-4", "doc-5"})

	req := searchReq(model.SearchModeHybrid)
	req.Facets = []string{"lifecycle_state", "author"}
	res, err := svc.Search(t.Context(), req)
	require.NoError(t, err)

	require.EqualValues(t, 5, res.TotalCount)

	var lifecycleTotal int64
	for _, b := range res.Facets["lifecycle_state"] {
		lifecycleTotal += b.Count
		require.NotEmpty(t, b.Value, "a blank facet value is what the un-hydrated rows produced")
	}
	require.EqualValues(t, res.TotalCount, lifecycleTotal,
		"facet counts must add up to the reported total")

	authors := map[string]int64{}
	for _, b := range res.Facets["author"] {
		authors[b.Value] = b.Count
	}
	require.Equal(t, map[string]int64{"Alice": 2, "Bob": 1, "Carol": 2}, authors,
		"the author facet must include the documents only the vector branch found")
}

// TestSearch_FacetsFallBackToAggsWhenPaginated pins the other half of the
// facet decision: once the result set spans more than one page, buckets
// derived from a single page would understate the corpus, so OpenSearch's
// aggregation over the whole lexical match set stays authoritative.
func TestSearch_FacetsFallBackToAggsWhenPaginated(t *testing.T) {
	f := &fakeOS{
		lexTotal: 100, // far more matches than this page holds
		lexDocs: []osDoc{
			{ID: "doc-1", Title: "Invoice A", Lifecycle: "active", Author: "Alice", SizeBytes: 100, CreatedAt: "2026-01-01T00:00:00Z"},
		},
		lexAggs: map[string]any{
			"lifecycle_state": map[string]any{"buckets": []any{
				map[string]any{"key": "active", "doc_count": 100.0},
			}},
		},
		hydrateDocs: []osDoc{
			{ID: "doc-4", Title: "Payment advice", Lifecycle: "draft", Author: "Carol", SizeBytes: 400, CreatedAt: "2026-02-01T00:00:00Z"},
		},
	}
	svc := newHarness(t, f, []string{"doc-4"})

	req := searchReq(model.SearchModeHybrid)
	req.Facets = []string{"lifecycle_state"}
	res, err := svc.Search(t.Context(), req)
	require.NoError(t, err)

	require.EqualValues(t, 101, res.TotalCount)
	require.Len(t, res.Facets["lifecycle_state"], 1)
	require.EqualValues(t, 100, res.Facets["lifecycle_state"][0].Count,
		"a paginated set keeps the OpenSearch aggregation, not one page's worth of counts")
}

// TestSearch_EmptyResultsSerializeAsArray pins the `[]` (not `null`)
// contract on an empty result set.
func TestSearch_EmptyResultsSerializeAsArray(t *testing.T) {
	f := &fakeOS{lexTotal: 0}
	svc := newHarness(t, f, nil)

	res, err := svc.Search(t.Context(), searchReq(model.SearchModeLexical))
	require.NoError(t, err)

	b, err := json.Marshal(res)
	require.NoError(t, err)
	require.Contains(t, string(b), `"results":[]`)
	require.NotContains(t, string(b), `"results":null`)
}

// TestSearch_LexicalPathUnchanged guards the branch that always worked:
// no vector round-trip, OpenSearch's own total, hydrated rows.
func TestSearch_LexicalPathUnchanged(t *testing.T) {
	f := &fakeOS{
		lexTotal: 42,
		lexDocs: []osDoc{
			{ID: "doc-1", Title: "Invoice A", Lifecycle: "active", Author: "Alice", SizeBytes: 100, CreatedAt: "2026-01-01T00:00:00Z"},
		},
	}
	svc := newHarness(t, f, []string{"doc-9"})

	res, err := svc.Search(t.Context(), searchReq(model.SearchModeLexical))
	require.NoError(t, err)

	require.Len(t, res.Results, 1)
	require.EqualValues(t, 42, res.TotalCount, "lexical keeps OpenSearch's full match count")
	require.Equal(t, "Invoice A", res.Results[0].Title)

	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range f.bodies {
		_, isHydration := idsFromBody(b)
		require.False(t, isHydration, "lexical mode must not run the vector hydration query")
	}
}

// TestMapHit_ZeroCreatedAtBecomesNull is the read-side half of BUG-04:
// documents indexed before the fix hold a literal "0001-01-01T00:00:00Z";
// echoing that back rendered as a year-1 date on every card.
func TestMapHit_ZeroCreatedAtBecomesNull(t *testing.T) {
	hit := mapHit(opensearch.RawHit{Source: map[string]any{
		"document_id": "doc-1",
		"title":       "Legacy row",
		"created_at":  "0001-01-01T00:00:00Z",
		"updated_at":  "0001-01-01T00:00:00Z",
	}})
	require.Nil(t, hit.CreatedAt, "the Go zero time is not a date")
	require.Nil(t, hit.UpdatedAt)

	b, err := json.Marshal(hit)
	require.NoError(t, err)
	require.NotContains(t, string(b), "0001-01-01")
}
