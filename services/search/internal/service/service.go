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
	log   zerolog.Logger
}

// Config is the DI struct for New.
type Config struct {
	OS     *opensearch.RealClient
	Repo   *repository.Repository
	Redis  *redis.Client
	Logger zerolog.Logger
}

// New constructs a Service.
func New(cfg Config) *Service {
	return &Service{
		os:    cfg.OS,
		repo:  cfg.Repo,
		redis: cfg.Redis,
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

// Search executes the query against OpenSearch and returns the results.
func (s *Service) Search(ctx context.Context, req *model.SearchRequest) (*model.SearchResult, error) {
	start := time.Now()

	query := opensearch.BuildSearchQuery(req)
	raw, err := s.os.Search(ctx, req.TenantID, query)
	if err != nil {
		return nil, fmt.Errorf("opensearch search: %w", err)
	}

	result := &model.SearchResult{
		TotalCount: raw.TotalHits,
		SearchMode: "lexical",
		LatencyMS:  time.Since(start).Milliseconds(),
	}

	for _, h := range raw.Hits {
		hit := mapHit(h)
		result.Results = append(result.Results, hit)
	}

	if len(raw.Aggs) > 0 {
		result.Facets = make(map[string][]model.FacetBucket)
		for k, buckets := range raw.Aggs {
			for _, b := range buckets {
				result.Facets[k] = append(result.Facets[k], model.FacetBucket{
					Value: b.Key,
					Count: b.DocCount,
				})
			}
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
