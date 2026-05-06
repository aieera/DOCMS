// Package service contains the business-logic layer for the search service.
// It orchestrates OpenSearch queries, Redis caching, and saved-search
// persistence without knowing about HTTP or gRPC.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/services/search/internal/model"
	"github.com/vaultdms/vaultdms/services/search/internal/opensearch"
	"github.com/vaultdms/vaultdms/services/search/internal/repository"
	"github.com/vaultdms/vaultdms/services/search/internal/vector"
)

const (
	recentSearchesPrefix = "recent_searches:"
	recentSearchesMax    = 50
)

// Service is the search facade.
type Service struct {
	os    *opensearch.RealClient
	repo  *repository.Repository
	redis *redis.Client
	// vec is the dense-vector path (§7.1 / D6 part 2). Nil in dev
	// stacks without intelligence running; Search() degrades to
	// lexical mode when it's absent.
	vec   *vector.Client
	log   zerolog.Logger
}

// Config is the DI struct for New.
type Config struct {
	OS     *opensearch.RealClient
	Repo   *repository.Repository
	Redis  *redis.Client
	Vector *vector.Client // optional
	Logger zerolog.Logger
}

// New constructs a Service.
func New(cfg Config) *Service {
	return &Service{
		os:    cfg.OS,
		repo:  cfg.Repo,
		redis: cfg.Redis,
		vec:   cfg.Vector,
		log:   cfg.Logger,
	}
}

// ---- GDPR subject erase (Wave 12.4) ---------------------------------------

// PurgeSubject deletes every document where `created_by` is
// subjectID. Matches the shape of the DSR erase workflow — the
// worker calls this via the search service's HTTP endpoint
// (registered in handler.go) for each tenant where the subject
// had content.
//
// Returns the OpenSearch `deleted` count so the privacy ledger
// can record an exact row-count.
func (s *Service) PurgeSubject(ctx context.Context, tenantID, subjectID string) (int64, error) {
	query := map[string]any{
		"query": map[string]any{
			"term": map[string]any{"created_by": subjectID},
		},
	}
	deleted, err := s.os.DeleteByQuery(ctx, tenantID, query)
	if err != nil {
		return 0, fmt.Errorf("opensearch delete-by-query: %w", err)
	}
	s.log.Info().Str("tenant_id", tenantID).Str("subject_id", subjectID).Int64("deleted", deleted).Msg("dsr subject erase: opensearch")
	return deleted, nil
}

// ---- Full-text search -----------------------------------------------------

// Search executes the query against OpenSearch and (for hybrid /
// semantic modes) Qdrant, fusing the results per blueprint §7.1.
// Current implementation: the BM25 path is fully wired; the dense
// vector path is a stub that returns an empty list until the
// intelligence-service /internal/v1/embed-query endpoint lands. When
// the stub returns nothing, hybrid mode degrades to lexical and logs
// a warning so dashboards can surface the degradation.
func (s *Service) Search(ctx context.Context, req *model.SearchRequest) (*model.SearchResult, error) {
	start := time.Now()

	mode := model.NormalizeMode(req.Mode)

	query := opensearch.BuildSearchQuery(req)

	// ADR 0065 — facet pipeline. Two short-circuits:
	// 1. Cache hit: build query without aggs, take buckets from Redis.
	// 2. Skip-flag hit (this shape was over the threshold within
	//    the last 60s): same — drop aggs, return facets={}.
	// Cache miss + facets requested: run a Count first; if over
	// threshold, drop aggs and set the skip flag for next time.
	var cachedFacets map[string][]model.FacetBucket
	var cacheKey string
	facetsRequested := len(req.Facets) > 0
	if facetsRequested {
		cacheKey = facetCacheKey(req)
		if s.shouldSkipFacets(ctx, cacheKey) {
			query = opensearch.StripAggs(query)
			s.log.Debug().Str("cache_key", stringForLogging(cacheKey)).
				Msg("facets skip-flag hit; aggs dropped")
		} else if cached := s.loadCachedFacets(ctx, cacheKey); cached != nil {
			cachedFacets = cached
			query = opensearch.StripAggs(query)
		} else {
			// Pre-flight: count the filtered hit set. If it's huge,
			// skip the aggregation — the user gets a fast response
			// with empty facets, and the skip-flag spares the next
			// caller the same probe.
			countBody := opensearch.QueryOnlyBody(query)
			if total, err := s.os.Count(ctx, req.TenantID, countBody); err == nil &&
				total > facetSkipThresholdValue() {
				query = opensearch.StripAggs(query)
				s.markFacetsSkipped(ctx, cacheKey)
				s.log.Info().Int64("total", total).
					Msg("facet skip threshold exceeded; aggs dropped")
			}
		}
	}

	raw, err := s.os.Search(ctx, req.TenantID, query)
	if err != nil {
		return nil, fmt.Errorf("opensearch search: %w", err)
	}

	// §7.1 / D6 — hybrid / semantic branches. The RRF fusion math
	// lives in services/search/internal/fusion; this layer owns
	// orchestration (parallel querying, score merge, pagination).
	if mode == model.SearchModeHybrid || mode == model.SearchModeSemantic {
		sem, err := s.semanticSearch(ctx, req)
		if err != nil {
			s.log.Warn().Err(err).Str("mode", mode).
				Msg("semantic path failed; degrading to lexical-only")
			mode = model.SearchModeLexical
		} else if len(sem) == 0 {
			// Nothing to fuse. Leave mode unchanged so the response
			// still reports what the client asked for, but the
			// result set is identical to lexical.
			s.log.Debug().Str("mode", mode).Msg("semantic path returned 0 hits")
		} else if mode == model.SearchModeHybrid {
			raw = fuseHits(raw, sem)
		} else {
			raw = semToRaw(sem)
		}
	}

	result := &model.SearchResult{
		TotalCount: raw.TotalHits,
		SearchMode: mode,
		LatencyMS:  time.Since(start).Milliseconds(),
	}

	for _, h := range raw.Hits {
		hit := mapHit(h)
		result.Results = append(result.Results, hit)
	}

	if cachedFacets != nil {
		// Cache hit — buckets came from Redis, OpenSearch ran without
		// aggs to skip the bucket compute.
		result.Facets = cachedFacets
	} else if len(raw.Aggs) > 0 {
		result.Facets = make(map[string][]model.FacetBucket)
		for k, buckets := range raw.Aggs {
			for _, b := range buckets {
				result.Facets[k] = append(result.Facets[k], model.FacetBucket{
					Value: b.Key,
					Count: b.DocCount,
				})
			}
		}
		// Fresh aggs — populate the cache for the next 60s of
		// repeat-the-query traffic. Done after we've returned the
		// data to the caller logically; storeCachedFacets is
		// best-effort and never blocks the response.
		if facetsRequested && cacheKey != "" {
			s.storeCachedFacets(ctx, cacheKey, result.Facets)
		}
	}

	pageSize := req.PageSize
	if pageSize <= 0 || pageSize > opensearch.MaxPageSize {
		pageSize = opensearch.DefaultPageSize
	}
	nextFrom := opensearch.DecodePageTokenInt(req.PageToken) + pageSize
	if int64(nextFrom) < raw.TotalHits {
		result.PageToken = opensearch.EncodePageToken(nextFrom)
	}

	// Store query in recent searches (fire-and-forget).
	if req.Query != "" {
		s.storeRecentSearch(ctx, req.TenantID, req.UserID, req.Query)
	}

	return result, nil
}

// ---- ADR 0067 grouped suggester -------------------------------------------

// Suggest returns the §7.5 grouped autocomplete shape — separate
// Documents/Tags/People rows plus the user's recent searches. One
// OpenSearch round-trip + one Redis read; sub-50ms p99 budget.
//
// Permission scope is the same bool.filter the main /search uses
// (tenant_id + ADR 0066 split-readable_by), so suggestions never
// surface values from docs the user can't read.
func (s *Service) Suggest(ctx context.Context, req *model.SearchRequest, q string, limit int) (*model.SuggestResult, error) {
	if limit <= 0 {
		limit = opensearch.DefaultSuggestLimit
	}
	if limit > opensearch.MaxSuggestLimit {
		limit = opensearch.MaxSuggestLimit
	}

	result := &model.SuggestResult{
		// Pre-allocate to empty so the JSON serializes as `[]`, not
		// `null`. The frontend renders section headers without a
		// per-field nil check.
		Documents: []model.DocumentSuggestion{},
		Tags:      []model.ValueSuggestion{},
		People:    []model.ValueSuggestion{},
		Recent:    []model.RecentSuggestion{},
	}

	// Recent searches first — Redis is much faster than OS, so
	// readying this list while the OS query is in flight gives the
	// caller a populated `recent` even if OS is briefly unhappy.
	for _, r := range s.getRecentSearches(ctx, req.TenantID, req.UserID, limit) {
		if q == "" || containsPrefix(r, q) {
			result.Recent = append(result.Recent, model.RecentSuggestion{Text: r})
			if len(result.Recent) >= limit {
				break
			}
		}
	}

	// Empty q + no recent matches → return early. The OS prefix-only
	// suggester would return the top-N globally on an empty prefix,
	// which is not what we want for this shape.
	if q == "" {
		return result, nil
	}

	body := opensearch.BuildSuggestQuery(req, q, limit)
	raw, err := s.os.Search(ctx, req.TenantID, body)
	if err != nil {
		// OS failure shouldn't blow up the whole response — recent
		// searches are still useful. Log + return what we have.
		s.log.Warn().Err(err).Str("q", q).Msg("suggest opensearch query failed")
		return result, nil
	}

	// Dedupe by title — a tenant with N copies of "Contract A" should
	// surface ONE row per title, not N. The first hit for a title
	// wins (highest-scored, since OS returns sorted by relevance).
	// The UI navigates to that doc on click; users can drill into
	// the Search page to see the rest.
	seenTitles := make(map[string]bool)
	for _, hit := range raw.Hits {
		title, _ := hit.Source["title"].(string)
		docID, _ := hit.Source["document_id"].(string)
		if title == "" || docID == "" || seenTitles[title] {
			continue
		}
		seenTitles[title] = true
		result.Documents = append(result.Documents, model.DocumentSuggestion{
			Text:       title,
			DocumentID: docID,
			Score:      hit.Score,
		})
		if len(result.Documents) >= limit {
			break
		}
	}

	for _, b := range raw.Aggs["tags_prefix"] {
		result.Tags = append(result.Tags, model.ValueSuggestion{Text: b.Key, Count: b.DocCount})
	}
	for _, b := range raw.Aggs["authors_prefix"] {
		result.People = append(result.People, model.ValueSuggestion{Text: b.Key, Count: b.DocCount})
	}

	return result, nil
}

// ---- Autocomplete ---------------------------------------------------------

// Autocomplete combines index suggestions with recent searches from Redis.
func (s *Service) Autocomplete(ctx context.Context, tenantID, userID string, groupIDs []string, q string, limit int) (*model.AutocompleteResult, error) {
	if limit <= 0 || limit > 20 {
		limit = 10
	}

	result := &model.AutocompleteResult{}
	seen := map[string]bool{}

	// 1. Recent searches from Redis.
	recents := s.getRecentSearches(ctx, tenantID, userID, limit)
	for _, r := range recents {
		if len(q) > 0 && !containsPrefix(r, q) {
			continue
		}
		if !seen[r] {
			result.Suggestions = append(result.Suggestions, model.Suggestion{Text: r, Source: "recent"})
			seen[r] = true
		}
	}

	// 2. Index suggestions via title.autocomplete.
	query := opensearch.BuildAutocompleteQuery(tenantID, userID, groupIDs, q, limit)
	raw, err := s.os.Search(ctx, tenantID, query)
	if err != nil {
		s.log.Warn().Err(err).Msg("autocomplete index query failed")
	} else {
		for _, h := range raw.Hits {
			title := strFromSource(h.Source, "title")
			if title != "" && !seen[title] {
				result.Suggestions = append(result.Suggestions, model.Suggestion{Text: title, Source: "index"})
				seen[title] = true
			}
			if len(result.Suggestions) >= limit {
				break
			}
		}
	}

	if len(result.Suggestions) > limit {
		result.Suggestions = result.Suggestions[:limit]
	}
	return result, nil
}

// ---- Saved searches -------------------------------------------------------

// CreateSavedSearch persists a saved search.
func (s *Service) CreateSavedSearch(ctx context.Context, ss *model.SavedSearch) error {
	ss.ID = repository.NewID()
	ss.CreatedAt = time.Now().UTC()
	return s.repo.CreateSavedSearch(ctx, ss)
}

// ListSavedSearches returns the user's saved searches.
func (s *Service) ListSavedSearches(ctx context.Context, tenantID, userID string) ([]*model.SavedSearch, error) {
	return s.repo.ListSavedSearches(ctx, tenantID, userID)
}

// DeleteSavedSearch removes a saved search.
func (s *Service) DeleteSavedSearch(ctx context.Context, tenantID, userID, id string) error {
	return s.repo.DeleteSavedSearch(ctx, tenantID, userID, id)
}

// ---- Indexing (called by the NATS consumer) -------------------------------

// IndexDocument upserts a document into OpenSearch.
func (s *Service) IndexDocument(ctx context.Context, doc *model.IndexDocument) error {
	return s.os.Index(ctx, doc)
}

// DeleteDocument removes a document from the index.
func (s *Service) DeleteDocument(ctx context.Context, tenantID, documentID string) error {
	return s.os.Delete(ctx, tenantID, documentID)
}

// PartialUpdate applies a partial update to an indexed document.
func (s *Service) PartialUpdate(ctx context.Context, tenantID, documentID string, fields map[string]any) error {
	return s.os.PartialUpdate(ctx, tenantID, documentID, fields)
}

// UpdateReadableByFolder re-indexes readable_by for all docs in a folder.
func (s *Service) UpdateReadableByFolder(ctx context.Context, tenantID, folderID string, readableBy []string) error {
	return s.os.UpdateByQuery(ctx, tenantID, map[string]any{
		"query": map[string]any{
			"bool": map[string]any{
				"filter": []any{
					map[string]any{"term": map[string]any{"tenant_id": tenantID}},
					map[string]any{"term": map[string]any{"folder_id": folderID}},
				},
			},
		},
		"script": map[string]any{
			"source": "ctx._source.readable_by = params.readable_by",
			"lang":   "painless",
			"params": map[string]any{"readable_by": readableBy},
		},
	})
}

// UpdateReadableByWorkspace re-indexes readable_by for all docs in a workspace.
func (s *Service) UpdateReadableByWorkspace(ctx context.Context, tenantID, workspaceID string, readableBy []string) error {
	return s.os.UpdateByQuery(ctx, tenantID, map[string]any{
		"query": map[string]any{
			"bool": map[string]any{
				"filter": []any{
					map[string]any{"term": map[string]any{"tenant_id": tenantID}},
					map[string]any{"term": map[string]any{"workspace_id": workspaceID}},
				},
			},
		},
		"script": map[string]any{
			"source": "ctx._source.readable_by = params.readable_by",
			"lang":   "painless",
			"params": map[string]any{"readable_by": readableBy},
		},
	})
}

// ---- Redis helpers --------------------------------------------------------

func (s *Service) storeRecentSearch(ctx context.Context, tenantID, userID, query string) {
	key := recentSearchesPrefix + tenantID + ":" + userID
	score := float64(time.Now().UnixMilli())
	pipe := s.redis.Pipeline()
	pipe.ZAdd(ctx, key, redis.Z{Score: score, Member: query})
	pipe.ZRemRangeByRank(ctx, key, 0, int64(-recentSearchesMax-1))
	pipe.Expire(ctx, key, 30*24*time.Hour)
	if _, err := pipe.Exec(ctx); err != nil {
		s.log.Warn().Err(err).Msg("store recent search")
	}
}

func (s *Service) getRecentSearches(ctx context.Context, tenantID, userID string, limit int) []string {
	key := recentSearchesPrefix + tenantID + ":" + userID
	result, err := s.redis.ZRevRange(ctx, key, 0, int64(limit-1)).Result()
	if err != nil {
		return nil
	}
	return result
}

// ---- mappers --------------------------------------------------------------

func mapHit(h opensearch.RawHit) model.DocumentHit {
	src := h.Source
	hit := model.DocumentHit{
		DocumentID:     strFromSource(src, "document_id"),
		Title:          strFromSource(src, "title"),
		Description:    strFromSource(src, "description"),
		DocumentClass:  strFromSource(src, "document_class"),
		LifecycleState: strFromSource(src, "lifecycle_state"),
		WorkspaceID:    strFromSource(src, "workspace_id"),
		FolderID:       strFromSource(src, "folder_id"),
		CreatedBy:      strFromSource(src, "created_by"),
		CreatedByName:  strFromSource(src, "created_by_name"),
		MimeType:       strFromSource(src, "mime_type"),
		ContentSnippet: strFromSource(src, "content_snippet"),
		Score:          h.Score,
		Highlights:     h.Highlight,
	}
	if v, ok := src["size_bytes"].(float64); ok {
		hit.SizeBytes = int64(v)
	}
	if v, ok := src["version_count"].(float64); ok {
		hit.VersionCount = int(v)
	}
	if v, ok := src["has_thumbnail"].(bool); ok {
		hit.HasThumbnail = v
	}
	if v, ok := src["created_at"].(string); ok {
		hit.CreatedAt, _ = time.Parse(time.RFC3339, v)
	}
	if v, ok := src["updated_at"].(string); ok {
		t, _ := time.Parse(time.RFC3339, v)
		hit.UpdatedAt = &t
	}
	if tags, ok := src["tags"].([]any); ok {
		for _, t := range tags {
			if s, ok := t.(string); ok {
				hit.Tags = append(hit.Tags, s)
			}
		}
	}
	return hit
}

func strFromSource(src map[string]any, key string) string {
	if v, ok := src[key].(string); ok {
		return v
	}
	return ""
}

func containsPrefix(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := range prefix {
		if s[i] != prefix[i] && s[i] != prefix[i]+32 && s[i] != prefix[i]-32 {
			return false
		}
	}
	return true
}

// Silence import
var _ = json.Marshal
