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

// filterSemanticByACL re-verifies vector (semantic) hits against the
// AUTHORITATIVE OpenSearch ACL. The Qdrant payload's readable_by is only
// rewritten by the intelligence service on re-embed (content change), so a
// permission REVOKE leaves it stale and a raw semantic hit can name a document
// the caller may no longer view (Epic 9 #3). We keep only the hits whose
// document id still passes the tenant + readable_by filter in OpenSearch. Fails
// CLOSED: on an ACL-recheck error the vector hits are dropped rather than
// returned unverified.
func (s *Service) filterSemanticByACL(ctx context.Context, req *model.SearchRequest, sem []semanticHit) []semanticHit {
	if len(sem) == 0 {
		return sem
	}
	ids := make([]string, 0, len(sem))
	for _, h := range sem {
		ids = append(ids, h.DocumentID)
	}
	res, err := s.os.Search(ctx, req.TenantID, opensearch.BuildVisibilityByIDsQuery(req, ids))
	if err != nil {
		s.log.Warn().Err(err).Msg("semantic ACL re-check failed; dropping vector hits (fail closed)")
		return nil
	}
	visible := make(map[string]struct{}, len(res.Hits))
	for _, h := range res.Hits {
		visible[h.ID] = struct{}{}
	}
	out := make([]semanticHit, 0, len(sem))
	for _, h := range sem {
		if _, ok := visible[h.DocumentID]; ok {
			out = append(out, h)
		}
	}
	return out
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
