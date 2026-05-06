// ADR 0065 — Redis-backed facet cache + skip-when-large gate for the
// faceted-search path. 60s TTL on a per-(tenant, query, filters,
// facets, principals) hash; tenants on a large workspace can run a
// dashboard refresh loop without hammering OpenSearch with the same
// aggregation every second.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/vaultdms/vaultdms/services/search/internal/model"
)

const (
	// 60s per ADR 0065. Bucket counts on a hot dashboard are
	// inherently slightly stale; this is the right trade.
	facetCacheTTL = 60 * time.Second

	// Skip aggregations when the count gate exceeds this. 1M is a
	// comfortable knob — bucket aggregation cost is ~O(N log N) on
	// the doc set, and at this scale the UI's cardinality sliders
	// matter more than the exact count anyway.
	facetSkipThreshold int64 = 1_000_000

	facetCachePrefix    = "facets:"
	facetSkipFlagPrefix = "facets:skip:"
)

// facetCacheKey is the stable hash that keys both the cache hit and
// the skip-flag. ADR 0065: principals are part of the key so two
// users in the same tenant with different group memberships don't
// share buckets — a fresh user joining a group must see facet
// counts that include their newly-readable docs.
func facetCacheKey(req *model.SearchRequest) string {
	h := sha256.New()
	// Defensive: sort the slices so re-orderings of the same logical
	// request don't produce different cache keys. Stable across
	// client versions that happen to encode params in different
	// orders.
	groups := append([]string(nil), req.GroupIDs...)
	sort.Strings(groups)
	tags := append([]string(nil), req.Filters.Tags...)
	sort.Strings(tags)
	docClass := append([]string(nil), req.Filters.DocumentClass...)
	sort.Strings(docClass)
	lifecycle := append([]string(nil), req.Filters.LifecycleState...)
	sort.Strings(lifecycle)
	mime := append([]string(nil), req.Filters.MimeType...)
	sort.Strings(mime)
	facets := append([]string(nil), req.Facets...)
	sort.Strings(facets)

	keyParts := struct {
		Tenant         string            `json:"t"`
		User           string            `json:"u"`
		Groups         []string          `json:"g"`
		Query          string            `json:"q"`
		WorkspaceID    string            `json:"w"`
		FolderID       string            `json:"f"`
		DocClass       []string          `json:"dc"`
		Lifecycle      []string          `json:"ls"`
		Tags           []string          `json:"tg"`
		MimeType       []string          `json:"mt"`
		CreatedBy      string            `json:"cb"`
		HasContent     *bool             `json:"hc"`
		CreatedAfter   string            `json:"ca"`
		CreatedBefore  string            `json:"cb2"`
		SizeMin        *int64            `json:"sn"`
		SizeMax        *int64            `json:"sx"`
		CustomMetadata map[string]string `json:"cm"`
		Facets         []string          `json:"fc"`
	}{
		Tenant:         req.TenantID,
		User:           req.UserID,
		Groups:         groups,
		Query:          req.Query,
		WorkspaceID:    req.Filters.WorkspaceID,
		FolderID:       req.Filters.FolderID,
		DocClass:       docClass,
		Lifecycle:      lifecycle,
		Tags:           tags,
		MimeType:       mime,
		CreatedBy:      req.Filters.CreatedBy,
		HasContent:     req.Filters.HasContent,
		SizeMin:        req.Filters.SizeMinBytes,
		SizeMax:        req.Filters.SizeMaxBytes,
		CustomMetadata: req.Filters.CustomMetadata,
		Facets:         facets,
	}
	if req.Filters.CreatedAfter != nil {
		keyParts.CreatedAfter = req.Filters.CreatedAfter.Format(time.RFC3339)
	}
	if req.Filters.CreatedBefore != nil {
		keyParts.CreatedBefore = req.Filters.CreatedBefore.Format(time.RFC3339)
	}

	b, _ := json.Marshal(keyParts)
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

// loadCachedFacets returns the previously-cached buckets, or nil if
// there's no entry. Cache miss is the common case; we never block
// the search on a Redis failure.
func (s *Service) loadCachedFacets(ctx context.Context, key string) map[string][]model.FacetBucket {
	if s.redis == nil {
		return nil
	}
	val, err := s.redis.Get(ctx, facetCachePrefix+key).Bytes()
	if err == redis.Nil || err != nil {
		return nil
	}
	out := map[string][]model.FacetBucket{}
	if err := json.Unmarshal(val, &out); err != nil {
		return nil
	}
	return out
}

// storeCachedFacets writes the bucket map under a 60s TTL. Failures
// log-warn only — the user already saw the result, the cache is
// just a latency optimization.
func (s *Service) storeCachedFacets(ctx context.Context, key string, facets map[string][]model.FacetBucket) {
	if s.redis == nil || len(facets) == 0 {
		return
	}
	b, err := json.Marshal(facets)
	if err != nil {
		return
	}
	if err := s.redis.Set(ctx, facetCachePrefix+key, b, facetCacheTTL).Err(); err != nil {
		s.log.Warn().Err(err).Msg("facet cache store failed")
	}
}

// shouldSkipFacets returns true when a previous request for this
// shape already proved the hit count is over the threshold. Cached
// for 60s — the workspace might cross the threshold during the
// window if a bulk import lands, but bucket counts during a heavy
// import are off anyway.
func (s *Service) shouldSkipFacets(ctx context.Context, key string) bool {
	if s.redis == nil {
		return false
	}
	v, err := s.redis.Get(ctx, facetSkipFlagPrefix+key).Result()
	if err != nil {
		return false
	}
	return v == "1"
}

func (s *Service) markFacetsSkipped(ctx context.Context, key string) {
	if s.redis == nil {
		return
	}
	if err := s.redis.Set(ctx, facetSkipFlagPrefix+key, "1", facetCacheTTL).Err(); err != nil {
		s.log.Warn().Err(err).Msg("facet skip-flag store failed")
	}
}

// facetSkipThresholdValue exposes the threshold for tests; we don't
// want a magic 1_000_000 littered across the test file.
func facetSkipThresholdValue() int64 { return facetSkipThreshold }

// stringForLogging is a small helper kept here so the cache file
// owns its own debug-format logic.
func stringForLogging(key string) string {
	if len(key) > 12 {
		return fmt.Sprintf("%s…", key[:12])
	}
	return key
}
