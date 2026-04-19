package service

import (
	"context"

	"github.com/vaultdms/vaultdms/services/search/internal/fusion"
	"github.com/vaultdms/vaultdms/services/search/internal/model"
	"github.com/vaultdms/vaultdms/services/search/internal/opensearch"
)

// semanticHit is the minimal shape returned by the dense-vector path.
// Only document_id + score are required for fusion; the OpenSearch
// _source is re-used for rendering (so we don't need to duplicate
// the doc body in Qdrant).
type semanticHit struct {
	DocumentID string
	Score      float64
}

// semanticSearch calls Qdrant for the top-K nearest neighbours of the
// query embedding. Current implementation: stub that returns nil,nil
// because the intelligence-service /internal/v1/embed-query endpoint
// doesn't exist yet (§D6 follow-up). Returning a zero-length slice
// makes Search() degrade cleanly to lexical mode — no special-case
// error path needed.
//
// When the real path lands, it does:
//   1. POST /internal/v1/embed-query to intelligence with req.Query
//      → []float32 (vector dimension per blueprint §6.8).
//   2. Qdrant search with that vector, scoped to tenant_id payload
//      filter + the user's readable_by group set.
//   3. Convert top-K to []semanticHit{DocumentID, Score}.
func (s *Service) semanticSearch(_ context.Context, _ *model.SearchRequest) ([]semanticHit, error) {
	return nil, nil
}

// fuseHits merges OpenSearch BM25 results with semantic hits via
// Reciprocal Rank Fusion (blueprint §7.1). Returns a *RawSearchResult
// shaped like the BM25 output so downstream code (mapHit, pagination)
// is unchanged. TotalHits becomes the union cardinality.
//
// Docs present only in `lex` or only in `sem` are included at their
// weighted rank; docs in both accumulate score from both rankers.
// Hit source/highlights come from the OpenSearch hit when available,
// otherwise a minimal stub with document_id (caller can hydrate).
func fuseHits(lex *opensearch.RawSearchResult, sem []semanticHit) *opensearch.RawSearchResult {
	lexRanked := make([]fusion.RankedID, 0, len(lex.Hits))
	srcByID := make(map[string]opensearch.RawHit, len(lex.Hits))
	for i, h := range lex.Hits {
		docID := hitDocumentID(h)
		if docID == "" {
			continue
		}
		lexRanked = append(lexRanked, fusion.RankedID{ID: docID, Rank: i + 1})
		srcByID[docID] = h
	}

	semRanked := make([]fusion.RankedID, 0, len(sem))
	for i, h := range sem {
		if h.DocumentID == "" {
			continue
		}
		semRanked = append(semRanked, fusion.RankedID{ID: h.DocumentID, Rank: i + 1})
	}

	fused := fusion.Fuse(lexRanked, semRanked, fusion.Options{})

	out := &opensearch.RawSearchResult{
		Aggs: lex.Aggs, // facets come from BM25 side only; semantic isn't faceted.
	}
	for _, r := range fused {
		if h, ok := srcByID[r.ID]; ok {
			h.Score = r.Score
			out.Hits = append(out.Hits, h)
			continue
		}
		out.Hits = append(out.Hits, opensearch.RawHit{
			Source: map[string]any{"document_id": r.ID},
			Score:  r.Score,
		})
	}
	out.TotalHits = int64(len(out.Hits))
	return out
}

// semToRaw converts pure-semantic hits into a RawSearchResult so the
// SearchModeSemantic path reuses the same mapHit pipeline. No facets
// (Qdrant has no aggregation layer).
func semToRaw(sem []semanticHit) *opensearch.RawSearchResult {
	out := &opensearch.RawSearchResult{}
	for _, h := range sem {
		if h.DocumentID == "" {
			continue
		}
		out.Hits = append(out.Hits, opensearch.RawHit{
			Source: map[string]any{"document_id": h.DocumentID},
			Score:  h.Score,
		})
	}
	out.TotalHits = int64(len(out.Hits))
	return out
}

// hitDocumentID extracts the document_id out of a RawHit's _source.
// Empty string on missing / wrong type — caller filters those out.
func hitDocumentID(h opensearch.RawHit) string {
	if id, ok := h.Source["document_id"].(string); ok {
		return id
	}
	return ""
}
