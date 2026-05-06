// Package model holds internal domain types for the search service.
package model

import "time"

// SearchRequest is the service-layer input for a full-text search.
type SearchRequest struct {
	TenantID  string
	UserID    string
	GroupIDs  []string
	Query     string
	// §7.1 / D6 — Mode controls the ranker stack:
	//   "" or "lexical" : BM25 only (OpenSearch)
	//   "semantic"      : dense-vector ANN only (Qdrant)
	//   "hybrid"        : both, fused via RRF (α=0.6 lex, 0.4 sem, k=60)
	// Unknown values fall back to "lexical" so a stale client can't
	// silently get a degraded search.
	Mode      string
	Filters   SearchFilters
	Facets    []string
	SortBy    string // "relevance" | "created_at" | "updated_at" | "title" | "size_bytes"
	SortOrder string // "asc" | "desc"
	PageSize  int
	PageToken string
	Highlight bool
	Explain   bool
}

// SearchMode enumerates the valid Mode values. Use these constants
// instead of hard-coding strings so the compiler flags typos.
const (
	SearchModeLexical  = "lexical"
	SearchModeSemantic = "semantic"
	SearchModeHybrid   = "hybrid"
)

// NormalizeMode coerces an arbitrary client-supplied mode into one of
// the three valid values, defaulting to lexical. Applied at the edge
// so the service body can compare against the constants above.
func NormalizeMode(m string) string {
	switch m {
	case SearchModeSemantic, SearchModeHybrid, SearchModeLexical:
		return m
	default:
		return SearchModeLexical
	}
}

// SearchFilters mirrors every filterable field in the OpenSearch index.
type SearchFilters struct {
	WorkspaceID    string
	FolderID       string
	DocumentClass  []string
	LifecycleState []string
	Tags           []string
	CreatedAfter   *time.Time
	CreatedBefore  *time.Time
	MimeType       []string
	SizeMinBytes   *int64
	SizeMaxBytes   *int64
	CreatedBy      string
	// CreatedByName is the display-name field; the §7.2 "author"
	// facet maps to it. Filter shape mirrors the symbolic facet
	// name to keep URLs self-consistent.
	CreatedByName  []string
	// RegionPin is the data-residency keyword. Top-level field on
	// the index — cannot be routed through CustomMetadata which
	// adds the `custom_metadata.` prefix.
	RegionPin      []string
	CustomMetadata map[string]string
	HasContent     *bool
}

// SearchResult is returned by the search facade.
type SearchResult struct {
	Results    []DocumentHit          `json:"results"`
	Facets     map[string][]FacetBucket `json:"facets,omitempty"`
	TotalCount int64                  `json:"total_count"`
	PageToken  string                 `json:"page_token,omitempty"`
	LatencyMS  int64                  `json:"latency_ms"`
	SearchMode string                 `json:"search_mode"`
}

// DocumentHit is one search-result row.
type DocumentHit struct {
	DocumentID     string              `json:"document_id"`
	Title          string              `json:"title"`
	Description    string              `json:"description,omitempty"`
	Highlights     map[string][]string `json:"highlights,omitempty"`
	DocumentClass  string              `json:"document_class,omitempty"`
	LifecycleState string              `json:"lifecycle_state"`
	WorkspaceID    string              `json:"workspace_id"`
	FolderID       string              `json:"folder_id,omitempty"`
	Tags           []string            `json:"tags,omitempty"`
	CreatedBy      string              `json:"created_by"`
	CreatedByName  string              `json:"created_by_name"`
	CreatedAt      time.Time           `json:"created_at"`
	UpdatedAt      *time.Time          `json:"updated_at,omitempty"`
	SizeBytes      int64               `json:"size_bytes"`
	MimeType       string              `json:"mime_type"`
	HasThumbnail   bool                `json:"has_thumbnail"`
	VersionCount   int                 `json:"version_count"`
	Score          float64             `json:"score"`
	ContentSnippet string              `json:"content_snippet,omitempty"`
}

// FacetBucket is one aggregation bucket.
type FacetBucket struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// AutocompleteResult wraps completions + recent searches.
type AutocompleteResult struct {
	Suggestions []Suggestion `json:"suggestions"`
}

// Suggestion is a single autocomplete entry.
type Suggestion struct {
	Text   string `json:"text"`
	Source string `json:"source"` // "index" | "recent"
}

// SavedSearch represents a user's saved query persisted in Postgres.
type SavedSearch struct {
	ID                    string        `json:"id"`
	TenantID              string        `json:"tenant_id"`
	UserID                string        `json:"user_id"`
	Name                  string        `json:"name"`
	Query                 string        `json:"query"`
	Filters               SearchFilters `json:"filters"`
	Notify                bool          `json:"notify"`
	NotifyIntervalMinutes int           `json:"notify_interval_minutes,omitempty"`
	CreatedAt             time.Time     `json:"created_at"`
	LastRunAt             *time.Time    `json:"last_run_at,omitempty"`
}

// IndexDocument is the shape upserted into OpenSearch. Field names match
// the index mapping exactly.
type IndexDocument struct {
	TenantID          string             `json:"tenant_id"`
	DocumentID        string             `json:"document_id"`
	WorkspaceID       string             `json:"workspace_id"`
	FolderID          string             `json:"folder_id,omitempty"`
	FolderPath        string             `json:"folder_path,omitempty"`
	Title             string             `json:"title"`
	Description       string             `json:"description,omitempty"`
	Content           string             `json:"content,omitempty"`
	ContentSnippet    string             `json:"content_snippet,omitempty"`
	Tags              []string           `json:"tags,omitempty"`
	DocumentClass     string             `json:"document_class,omitempty"`
	LifecycleState    string             `json:"lifecycle_state"`
	RegionPin         string             `json:"region_pin,omitempty"`
	MimeType          string             `json:"mime_type,omitempty"`
	SizeBytes         int64              `json:"size_bytes"`
	CreatedBy         string             `json:"created_by"`
	CreatedByName     string             `json:"created_by_name"`
	CreatedAt         time.Time          `json:"created_at"`
	UpdatedAt         *time.Time         `json:"updated_at,omitempty"`
	CustomMetadata    map[string]any     `json:"custom_metadata,omitempty"`
	// ReadableBy is the legacy mixed field — user_ids, group_ids,
	// and "everyone" all in one keyword. Kept populated for the
	// migration window so docs indexed before ADR 0066 still match
	// queries from clients that already use the split fields.
	ReadableBy        []string           `json:"readable_by"`
	// ReadableByUsers — direct grants + group memberships expanded
	// to individual user_ids. Owned by the document service's
	// publish-time expansion (ADR 0066 §"Indexer split"). Falls back
	// to ReadableBy when an upstream hasn't been updated yet.
	ReadableByUsers   []string           `json:"readable_by_users,omitempty"`
	// ReadableByGroups — group_ids the doc grants access to (no
	// expansion). Used so a query for a user newly added to a group
	// matches without having to wait for the doc's reindex.
	ReadableByGroups  []string           `json:"readable_by_groups,omitempty"`
	// ShareTokens — opaque token strings for unauthenticated shared
	// links. Separate from user/group access so revoking a share
	// link doesn't have to re-publish the whole readable_by set.
	ShareTokens       []string           `json:"share_tokens,omitempty"`
	HasThumbnail      bool               `json:"has_thumbnail"`
	VersionCount      int                `json:"version_count"`
	ExtractedEntities *ExtractedEntities `json:"extracted_entities,omitempty"`
}

// ExtractedEntities are NER results stored per-document.
type ExtractedEntities struct {
	People        []string `json:"people,omitempty"`
	Organizations []string `json:"organizations,omitempty"`
	Locations     []string `json:"locations,omitempty"`
	Dates         []string `json:"dates,omitempty"`
	Amounts       []string `json:"amounts,omitempty"`
}
