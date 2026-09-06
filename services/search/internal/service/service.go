// Package service contains the business-logic layer for the search service.
// It orchestrates OpenSearch queries, Redis caching, and saved-search
// persistence without knowing about HTTP or gRPC.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/services/search/internal/model"
	"github.com/aieera/sedoc/services/search/internal/opensearch"
	"github.com/aieera/sedoc/services/search/internal/repository"
	"github.com/aieera/sedoc/services/search/internal/vector"
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
	vec *vector.Client
	log zerolog.Logger
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

// ResolveUserGroups returns the caller's ACL group ids authoritatively from the
// DB (group_members), used as the fallback when the request had no session-
// loaded groups (e.g. an internal-service caller). Never trusts a client header.
func (s *Service) ResolveUserGroups(ctx context.Context, tenantID, userID string) []string {
	g, err := s.repo.GroupsForUser(ctx, tenantID, userID)
	if err != nil {
		s.log.Warn().Err(err).Msg("resolve user groups failed; treating as no groups")
		return nil
	}
	return g
}

// ResolveUserWorkspaces returns the caller's workspace memberships
// authoritatively from the DB (workspace_members). Same rationale as
// ResolveUserGroups.
func (s *Service) ResolveUserWorkspaces(ctx context.Context, tenantID, userID string) []string {
	w, err := s.repo.WorkspacesForUser(ctx, tenantID, userID)
	if err != nil {
		s.log.Warn().Err(err).Msg("resolve user workspaces failed; treating as none")
		return nil
	}
	return w
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
// Both paths are fully wired: the BM25 path always runs; the dense
// vector path runs when s.vec is configured (SEDOC_INTELLIGENCE_EMBED_URL
// + SEDOC_QDRANT_URL set) and the query is non-empty. When the vector
// path errors or returns nothing (no s.vec, embed/Qdrant unreachable,
// or zero semantic hits), hybrid mode degrades to lexical-only and
// stamps result.Degraded so dashboards can surface the degradation.
func (s *Service) Search(ctx context.Context, req *model.SearchRequest) (*model.SearchResult, error) {
	start := time.Now()

	mode := model.NormalizeMode(req.Mode)

	query := opensearch.BuildSearchQuery(req)

	// ADR 0082 — facet pipeline. Two short-circuits:
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
	//
	// ADR 0111 — when the vector path errors (timeout, intelligence
	// down, Qdrant unreachable) we degrade to lexical-only and
	// preserve the requested mode so the response.SearchMode tells
	// the caller WHAT they asked for, while response.Degraded names
	// WHAT actually ran. The HTTP handler stamps an
	// X-Search-Mode-Degraded header from Degraded.
	requestedMode := mode
	degradedTo := ""
	// vectorFused records that the returned hit set actually contains
	// dense-vector rows. Facets then have to be derived from those rows:
	// the OpenSearch aggregations only describe the BM25 half, which is
	// what made facet counts contradict the reported total.
	vectorFused := false
	if mode == model.SearchModeHybrid || mode == model.SearchModeSemantic {
		sem, err := s.semanticSearch(ctx, req)
		if err != nil {
			s.log.Warn().Err(err).Str("mode", requestedMode).
				Msg("semantic path failed; degrading to lexical-only")
			degradedTo = model.SearchModeLexical
		} else {
			// ONE shared step: re-verify against the authoritative OpenSearch
			// ACL (the Qdrant payload goes stale after a permission revoke,
			// Epic 9 #3), re-apply the request's own filters, and pull the
			// same _source the lexical branch renders from.
			var hydrated map[string]opensearch.RawHit
			sem, hydrated = s.hydrateSemantic(ctx, req, sem)
			switch {
			case len(sem) == 0:
				// Nothing to fuse. Hybrid still works (lexical-only fusion =
				// lexical-only); semantic-only got nothing so the result set is
				// empty either way. Mark as degraded so dashboards can count
				// silent "zero semantic" runs.
				s.log.Debug().Str("mode", requestedMode).Msg("semantic path returned 0 hits")
				degradedTo = model.SearchModeLexical
			case requestedMode == model.SearchModeHybrid:
				raw = fuseHits(raw, sem, hydrated)
				vectorFused = true
			default:
				raw = semToRaw(sem, hydrated)
				vectorFused = true
			}
		}
	}

	result := &model.SearchResult{
		// Non-nil so an empty result set serializes as `[]`, not `null`.
		Results:    []model.DocumentHit{},
		TotalCount: raw.TotalHits,
		SearchMode: requestedMode,
		LatencyMS:  time.Since(start).Milliseconds(),
		Degraded:   degradedTo,
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

	// The OpenSearch aggregations above are computed over the BM25 match set
	// only — Qdrant has no aggregation layer, so a fused/semantic response
	// carried buckets describing a different (smaller) set of documents than
	// the rows and total it reported: the sidebar said 3 while the header
	// said 8, and semantic mode had no facets at all.
	//
	// When the response holds the COMPLETE result set (everything fits on
	// this page — always true for semantic, and true for hybrid on small
	// corpora), derive the buckets from the returned rows so the counts add
	// up to total_count exactly. When the set is paginated, OpenSearch's
	// aggregation over the whole lexical match set remains the better
	// answer; deriving from one page would understate it badly. Derived
	// buckets are merged over the aggregation ones so a facet shape that
	// can't be derived (date_histogram, range) keeps its OpenSearch buckets.
	//
	// Deliberately AFTER the cache store above, so only the mode-independent
	// BM25 buckets are ever cached.
	if vectorFused && facetsRequested && int64(len(result.Results)) == result.TotalCount {
		if derived := facetsFromHits(raw.Hits, req.Facets); derived != nil {
			if result.Facets == nil {
				result.Facets = derived
			} else {
				for name, buckets := range derived {
					result.Facets[name] = buckets
				}
			}
		}
	}

	// Deep pagination via search_after (Workstream 7): the next page's cursor is
	// the last hit's sort values. Only emit when the page came back full — a
	// short page means the end. Reachable past the 10k from+size ceiling because
	// the query carries no `from`.
	pageSize := req.PageSize
	if pageSize <= 0 || pageSize > opensearch.MaxPageSize {
		pageSize = opensearch.DefaultPageSize
	}
	if len(raw.Hits) == pageSize {
		last := raw.Hits[len(raw.Hits)-1]
		result.PageToken = opensearch.EncodeSearchAfter(last.Sort)
	}

	// Store query in recent searches (fire-and-forget).
	if req.Query != "" {
		s.storeRecentSearch(ctx, req.TenantID, req.UserID, req.Query)
	}

	return result, nil
}

// ---- ADR 0084 grouped suggester -------------------------------------------

// Suggest returns the §7.5 grouped autocomplete shape — separate
// Documents/Tags/People rows plus the user's recent searches. One
// OpenSearch round-trip + one Redis read; sub-50ms p99 budget.
//
// Permission scope is the same bool.filter the main /search uses
// (tenant_id + ADR 0083 split-readable_by), so suggestions never
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

	// Pre-allocated so an empty completion list serializes as `[]`, not
	// `null` — same contract as SuggestResult below.
	result := &model.AutocompleteResult{Suggestions: []model.Suggestion{}}
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

// ListSavedSearches returns the user's saved searches with embedded
// subscriber lists. ADR 0085: GET response always carries
// `subscribers[]` + `subscriber_count` so the UI can render the
// roster without a per-row fetch.
func (s *Service) ListSavedSearches(ctx context.Context, tenantID, userID string) ([]*model.SavedSearch, error) {
	rows, err := s.repo.ListSavedSearches(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}
	for _, ss := range rows {
		subs, err := s.repo.ListSubscribers(ctx, tenantID, ss.ID)
		if err != nil {
			s.log.Warn().Err(err).Str("id", ss.ID).Msg("list subscribers failed; continuing")
			continue
		}
		ss.Subscribers = subs
		ss.SubscriberCount = len(subs)
	}
	return rows, nil
}

// DeleteSavedSearch removes a saved search. Cascade-cancels the
// bound Temporal workflow and drops subscriber rows.
func (s *Service) DeleteSavedSearch(ctx context.Context, tenantID, userID, id string) error {
	// Subscribers cascade via direct delete (no FK cascade because
	// we want explicit control + audit). Best-effort; the saved-
	// search delete is the load-bearing operation.
	if err := s.repo.DeleteAllSubscribers(ctx, tenantID, id); err != nil {
		s.log.Warn().Err(err).Msg("subscriber cascade delete failed; continuing")
	}
	// The bound Temporal alert schedule is cancelled by the workflow
	// worker's reconcile loop (ReconcileSavedSearchAlertSchedules orphan
	// sweep) within one tick — the search service has no Temporal client,
	// so deleting the row here is the trigger; the schedule cleanup follows.
	return s.repo.DeleteSavedSearch(ctx, tenantID, userID, id)
}

// UpdateSavedSearch patches mutable fields. ADR 0085 §"PATCH".
// Returns ErrNotFound when the row doesn't belong to the user.
func (s *Service) UpdateSavedSearch(
	ctx context.Context,
	tenantID, userID, id string,
	patch repository.SavedSearchPatch,
) (*model.SavedSearch, error) {
	if err := s.repo.UpdateSavedSearch(ctx, tenantID, userID, id, patch); err != nil {
		return nil, err
	}
	ss, err := s.repo.GetSavedSearch(ctx, tenantID, userID, id)
	if err != nil {
		return nil, err
	}
	subs, _ := s.repo.ListSubscribers(ctx, tenantID, id)
	ss.Subscribers = subs
	ss.SubscriberCount = len(subs)
	return ss, nil
}

// AddSubscriber adds (or updates the channels of) a subscriber. The
// owner is implicitly subscribed when notify=true; this method is
// for ADDITIONAL subscribers — typically a team member or admin
// bulk-subscribe.
func (s *Service) AddSubscriber(
	ctx context.Context,
	tenantID, savedSearchID, userID, subscribedBy string,
	channels []string,
) error {
	if len(channels) == 0 {
		channels = []string{"in_app"}
	}
	return s.repo.AddSubscriber(ctx, tenantID, savedSearchID, userID, subscribedBy, channels)
}

// RemoveSubscriber drops a subscription row.
func (s *Service) RemoveSubscriber(
	ctx context.Context,
	tenantID, savedSearchID, userID string,
) error {
	return s.repo.RemoveSubscriber(ctx, tenantID, savedSearchID, userID)
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

// readableByScript builds a painless update that overwrites whichever of
// readable_by / readable_by_users / readable_by_groups are present in fields.
// All three MUST be kept in sync: the search ACL should-chain (opensearch/
// query.go buildFilters) matches on the SPLIT readable_by_groups/_users, so
// updating only the mixed readable_by — the old behaviour — left the split
// fields stale and a user in a REVOKED group kept matching the document
// (Epic 9 #2). Permission-change events already carry the split fields.
func readableByScript(fields map[string]any) map[string]any {
	var src strings.Builder
	params := map[string]any{}
	for _, f := range []string{"readable_by", "readable_by_users", "readable_by_groups"} {
		if v, ok := fields[f]; ok {
			src.WriteString("ctx._source." + f + " = params." + f + "; ")
			params[f] = v
		}
	}
	return map[string]any{"source": src.String(), "lang": "painless", "params": params}
}

// UpdateReadableByFolder re-indexes the readable_by* fields for all docs in a
// folder. fields carries readable_by plus (when present) the split
// readable_by_users / readable_by_groups.
func (s *Service) UpdateReadableByFolder(ctx context.Context, tenantID, folderID string, fields map[string]any) error {
	return s.os.UpdateByQuery(ctx, tenantID, map[string]any{
		"query": map[string]any{
			"bool": map[string]any{
				"filter": []any{
					map[string]any{"term": map[string]any{"tenant_id": tenantID}},
					map[string]any{"term": map[string]any{"folder_id": folderID}},
				},
			},
		},
		"script": readableByScript(fields),
	})
}

// UpdateReadableByWorkspace re-indexes the readable_by* fields for all docs in a
// workspace (same split-field sync as UpdateReadableByFolder).
func (s *Service) UpdateReadableByWorkspace(ctx context.Context, tenantID, workspaceID string, fields map[string]any) error {
	return s.os.UpdateByQuery(ctx, tenantID, map[string]any{
		"query": map[string]any{
			"bool": map[string]any{
				"filter": []any{
					map[string]any{"term": map[string]any{"tenant_id": tenantID}},
					map[string]any{"term": map[string]any{"workspace_id": workspaceID}},
				},
			},
		},
		"script": readableByScript(fields),
	})
}

// ---- Redis helpers --------------------------------------------------------

func (s *Service) storeRecentSearch(ctx context.Context, tenantID, userID, query string) {
	if s.redis == nil {
		// Same nil-tolerance the facet cache has: the recent-search
		// ledger is a convenience, never a reason to fail a search.
		return
	}
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
	if s.redis == nil {
		return nil
	}
	key := recentSearchesPrefix + tenantID + ":" + userID
	result, err := s.redis.ZRevRange(ctx, key, 0, int64(limit-1)).Result()
	if err != nil {
		return nil
	}
	return result
}

// ---- mappers --------------------------------------------------------------

func maskHighlights(h map[string][]string) map[string][]string {
	if len(h) == 0 {
		return h
	}
	out := make(map[string][]string, len(h))
	for field, frags := range h {
		m := make([]string, len(frags))
		for i, f := range frags {
			m[i] = MaskSensitive(f)
		}
		out[field] = m
	}
	return out
}

func mapHit(h opensearch.RawHit) model.DocumentHit {
	src := h.Source
	hit := model.DocumentHit{
		DocumentID:     strFromSource(src, "document_id"),
		Title:          strFromSource(src, "title"),
		Description:    MaskSensitive(strFromSource(src, "description")),
		DocumentClass:  strFromSource(src, "document_class"),
		LifecycleState: strFromSource(src, "lifecycle_state"),
		WorkspaceID:    strFromSource(src, "workspace_id"),
		FolderID:       strFromSource(src, "folder_id"),
		CreatedBy:      strFromSource(src, "created_by"),
		CreatedByName:  strFromSource(src, "created_by_name"),
		MimeType:       strFromSource(src, "mime_type"),
		// QA SD-22: snippets, descriptions and highlight fragments carry
		// extracted document content — mask cards/SSNs on the way out.
		// mapHit is the single funnel for lexical, semantic AND federated
		// results, so this covers every rendering path.
		ContentSnippet: MaskSensitive(strFromSource(src, "content_snippet")),
		Score:          h.Score,
		Highlights:     maskHighlights(h.Highlight),
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
		if t, err := parseSearchTime(v); err == nil && !t.IsZero() {
			hit.CreatedAt = &t
		}
	}
	if v, ok := src["updated_at"].(string); ok {
		if t, err := parseSearchTime(v); err == nil && !t.IsZero() {
			hit.UpdatedAt = &t
		}
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

// parseSearchTime tries the date formats OpenSearch can serialise dates
// in for our schema's "date" fields. A parsed-but-ZERO result is treated
// as "no date" by the caller: documents indexed before the indexer
// carried created_at hold a literal "0001-01-01T00:00:00Z" (Go's zero
// time, serialised by the old non-omitempty IndexDocument field), and
// echoing that back produced "0001-01-01T00:00:00Z" on every hit. Those
// rows are repaired by a reindex (docs/runbooks/search-reindex.md); until
// then the field is simply absent.
//
// The default mapping is strict_date_optional_time, which emits one of:
//   - 2026-05-20T10:30:00Z
//   - 2026-05-20T10:30:00.123Z
//   - 2026-05-20T10:30:00+00:00
//
// time.RFC3339 alone misses the millisecond variant, which produced
// hit.CreatedAt == zero-time and "2025 years ago" on the frontend.
func parseSearchTime(v string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z07:00"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised time format: %q", v)
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
