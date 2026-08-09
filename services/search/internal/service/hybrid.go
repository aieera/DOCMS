package service

import (
	"context"
	"time"

	"github.com/aieera/sedoc/services/search/internal/fusion"
	"github.com/aieera/sedoc/services/search/internal/model"
	"github.com/aieera/sedoc/services/search/internal/opensearch"
)

// ADR 0111 — wall-clock budget for the dense-vector path. The
// embedding-service call dominates this latency (typically 80-300ms
// for a sentence-level transformer). If embed+ANN can't return in
// 500ms we abandon the vector path entirely and serve lexical-only
// with a Degraded marker. Search is latency-sensitive; a slow
// Qdrant / intelligence pod must never block the response.
const semanticBudget = 500 * time.Millisecond

// semanticHit is the minimal shape returned by the dense-vector path.
// Only document_id + score are required for fusion; the OpenSearch
// _source is re-used for rendering (so we don't need to duplicate
// the doc body in Qdrant).
type semanticHit struct {
	DocumentID string
	Score      float64
}

// semanticSearch embeds the query via the intelligence service's
// /internal/v1/embed-query endpoint and ANN-queries Qdrant with a
// tenant_id + readable_by payload filter (blueprint §7.3). Returns
// nil hits + nil error when no vector client is wired (dev stacks
// without intelligence running) so hybrid mode degrades to lexical
// cleanly.
func (s *Service) semanticSearch(ctx context.Context, req *model.SearchRequest) ([]semanticHit, error) {
	if s.vec == nil || req.Query == "" {
		return nil, nil
	}
	// §7.3 SHARE-TOKEN ISOLATION (Epic 9 #4): an unauthenticated share-link
	// follower must see ONLY the shared document. The Qdrant payload filter is on
	// readable_by (user/group/everyone) and cannot be scoped to a share token,
	// and readablePrincipals injects "everyone" — so running the vector path here
	// would leak the whole tenant-wide-visible corpus (document ids + scores).
	// Skip it; the lexical path is already share-token-scoped, so hybrid/semantic
	// correctly degrades to the single shared doc.
	if isAnonymousShareFollower(req) {
		return nil, nil
	}
	limit := req.PageSize
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	// ADR 0111 — hard cap on the wall clock for embed + ANN. If the
	// parent context already has a tighter deadline, that wins
	// (e.g. a request-level timeout from the gateway).
	semCtx, cancel := context.WithTimeout(ctx, semanticBudget)
	defer cancel()
	raw, err := s.vec.SemanticSearch(semCtx, req.Query, req.TenantID, readablePrincipals(req), limit)
	if err != nil {
		// ctx.Deadline-exceeded surfaces as a deadline error; both
		// that and unexpected vector errors get the same caller-
		// visible treatment (fallback to lexical-only) — the caller
		// in Service.Search differentiates by reading semCtx.Err()
		// from the context, but at this layer they're equivalent.
		return nil, err
	}
	out := make([]semanticHit, 0, len(raw))
	for _, h := range raw {
		out = append(out, semanticHit{DocumentID: h.DocumentID, Score: h.Score})
	}
	return out, nil
}

// hydrateSemantic is the ONE step that turns raw vector candidates into
// renderable, authorized, filtered results. It runs a single OpenSearch query
// (opensearch.BuildHydrateByIDsQuery) over the candidate ids that applies the
// same ACL AND the same request filters as the lexical branch, and returns the
// full _source for the survivors.
//
// Returns (survivors in vector-rank order, id → hydrated hit). A candidate the
// query didn't return is dropped: it was deleted, revoked, or filtered out, and
// must appear in neither the results nor the total count.
//
// Fails CLOSED: on a hydration error every vector hit is dropped rather than
// returned unverified/blank.
func (s *Service) hydrateSemantic(
	ctx context.Context,
	req *model.SearchRequest,
	sem []semanticHit,
) ([]semanticHit, map[string]opensearch.RawHit) {
	if len(sem) == 0 {
		return sem, nil
	}
	ids := make([]string, 0, len(sem))
	for _, h := range sem {
		ids = append(ids, h.DocumentID)
	}
	res, err := s.os.Search(ctx, req.TenantID, opensearch.BuildHydrateByIDsQuery(req, ids))
	if err != nil {
		s.log.Warn().Err(err).Msg("semantic hydration failed; dropping vector hits (fail closed)")
		return nil, nil
	}
	hydrated := make(map[string]opensearch.RawHit, len(res.Hits))
	for _, h := range res.Hits {
		id := h.ID
		if id == "" {
			id = hitDocumentID(h)
		}
		if id == "" {
			continue
		}
		hydrated[id] = h
	}
	out := make([]semanticHit, 0, len(sem))
	for _, h := range sem {
		if _, ok := hydrated[h.DocumentID]; ok {
			out = append(out, h)
		}
	}
	return out, hydrated
}

// readablePrincipals builds the principal set the dense-vector path
// matches against the chunk payload's `readable_by` array. It mirrors
// the OpenSearch permission filter (opensearch/query.go): a chunk is
// visible when its readable_by contains the user, ANY of the user's
// groups, or the synthetic "everyone" group that marks tenant-wide
// readable documents. The vector payload collapses users + groups +
// everyone into one `readable_by` field, so the OR-of-clauses on the
// lexical side becomes a single match-any here.
//
// Without "everyone", public documents (readable_by=["everyone"]) were
// invisible to semantic/hybrid search even though lexical found them —
// the bug that made hybrid silently degrade to lexical-only for the
// common "shared with the whole tenant" case.
// isAnonymousShareFollower mirrors opensearch buildFilters' shareOnly condition:
// an unauthenticated share-link follower (no real user id) presenting a share
// token. The vector path must not run for them (see semanticSearch).
func isAnonymousShareFollower(req *model.SearchRequest) bool {
	return req.ShareToken != "" && (req.UserID == "" || req.UserID == "anonymous")
}

func readablePrincipals(req *model.SearchRequest) []string {
	out := make([]string, 0, len(req.GroupIDs)+2)
	if req.UserID != "" {
		out = append(out, req.UserID)
	}
	out = append(out, req.GroupIDs...)
	out = append(out, "everyone")
	return out
}

// fuseHits merges OpenSearch BM25 results with semantic hits via
// Reciprocal Rank Fusion (blueprint §7.1). Returns a *RawSearchResult
// shaped like the BM25 output so downstream code (mapHit, pagination)
// is unchanged.
//
// Docs present only in `lex` or only in `sem` are included at their
// weighted rank; docs in both accumulate score from both rankers.
// Source/highlights come from the OpenSearch hit for the lexical half and
// from `hydrated` (opensearch.BuildHydrateByIDsQuery) for the vector-only
// half — a fused row is NEVER a document_id-only stub. A vector id with no
// hydrated entry is dropped outright: it is deleted, no longer readable, or
// filtered out, and must not inflate the result set or the count.
//
// TotalHits is the union count over REAL rows: the lexical branch's own
// (already filtered, all-pages) total plus the hydrated vector hits that
// were not already on the lexical page. Both terms are post-filter, so an
// excluding filter yields 0 — never the pre-filter candidate count. (A
// vector hit that would have appeared on a LATER lexical page is counted
// twice; bounded by the vector limit and preferable to under-reporting.)
func fuseHits(lex *opensearch.RawSearchResult, sem []semanticHit, hydrated map[string]opensearch.RawHit) *opensearch.RawSearchResult {
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
		if _, ok := hydrated[h.DocumentID]; !ok {
			continue // unhydratable candidate — drop, don't render blank
		}
		semRanked = append(semRanked, fusion.RankedID{ID: h.DocumentID, Rank: i + 1})
	}

	fused := fusion.Fuse(lexRanked, semRanked, fusion.Options{})

	// Aggs are carried through as OpenSearch computed them — over the BM25
	// half only. The service replaces them with buckets derived from the
	// fused rows when the whole result set fits on this page (facetsFromHits).
	out := &opensearch.RawSearchResult{Aggs: lex.Aggs}
	semanticOnly := int64(0)
	for _, r := range fused {
		if h, ok := srcByID[r.ID]; ok {
			h.Score = r.Score
			out.Hits = append(out.Hits, h)
			continue
		}
		h, ok := hydrated[r.ID]
		if !ok {
			continue
		}
		h.Score = r.Score
		out.Hits = append(out.Hits, h)
		semanticOnly++
	}
	out.TotalHits = lex.TotalHits + semanticOnly
	return out
}

// semToRaw converts pure-semantic hits into a RawSearchResult so the
// SearchModeSemantic path reuses the same mapHit pipeline, taking each
// document's _source from the shared hydration step. Candidates that failed
// hydration are dropped, so TotalHits counts only rows the caller can
// actually render. No facets from OpenSearch (Qdrant has no aggregation
// layer) — the service derives them from the hits.
func semToRaw(sem []semanticHit, hydrated map[string]opensearch.RawHit) *opensearch.RawSearchResult {
	out := &opensearch.RawSearchResult{}
	for _, h := range sem {
		if h.DocumentID == "" {
			continue
		}
		hit, ok := hydrated[h.DocumentID]
		if !ok {
			continue
		}
		hit.Score = h.Score
		out.Hits = append(out.Hits, hit)
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
