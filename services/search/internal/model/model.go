// Package model holds internal domain types for the search service.
package model

import "time"

// SearchRequest is the service-layer input for a full-text search.
type SearchRequest struct {
	TenantID  string
	UserID    string
	GroupIDs  []string
	Query     string
	Filters   SearchFilters
	Facets    []string
	SortBy    string // "relevance" | "created_at" | "updated_at" | "title" | "size_bytes"
	SortOrder string // "asc" | "desc"
	PageSize  int
	PageToken string
	Highlight bool
	Explain   bool
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
	ReadableBy        []string           `json:"readable_by"`
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
