// Package model holds internal domain types for the search service.
package model

import "time"

// SearchRequest is the service-layer input for a full-text search.
type SearchRequest struct {
	TenantID  string
	UserID    string
	GroupIDs  []string
	// ShareToken — when non-empty, the request is from an unauthenticated
	// share-link follower. Mapped to a `terms share_tokens [token]`
	// match clause in addition to the user/group clauses, so a share
	// link's recipient can also be a logged-in user with no overlap.
	// Empty string means "no share-token path; only user/group access".
	ShareToken string
	Query      string
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
//
// ADR 0111 — `Degraded` is set when the caller asked for hybrid /
// semantic but the dense-vector path was skipped (embedding
// timeout, intelligence-service unreachable, Qdrant query error).
// The HTTP handler reflects this into an X-Search-Mode-Degraded
// response header so dashboards can surface the rate at which
// semantic results are silently missing from the fusion.
type SearchResult struct {
	Results    []DocumentHit          `json:"results"`
	Facets     map[string][]FacetBucket `json:"facets,omitempty"`
	TotalCount int64                  `json:"total_count"`
	PageToken  string                 `json:"page_token,omitempty"`
	LatencyMS  int64                  `json:"latency_ms"`
	SearchMode string                 `json:"search_mode"`
	// Degraded names the mode that actually ran. Empty string when
	// the requested mode matched what executed; otherwise the value
	// is the fallback mode the response was served with (typically
	// "lexical"). Surfaces as X-Search-Mode-Degraded in HTTP.
	Degraded   string                 `json:"degraded,omitempty"`
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
	// Pointer + omitempty so a parse failure (OpenSearch emits a date
	// format the mapper doesn't recognise) renders as null on the
	// frontend instead of the Go zero time (0001-01-01T00:00:00Z),
	// which formatRelativeTime previously surfaced as "2025 years ago".
	CreatedAt      *time.Time          `json:"created_at,omitempty"`
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

// SuggestResult is the ADR 0084 grouped autocomplete shape.
// Each group is independently capped at the request's `limit` so a
// no-tags / no-people corpus doesn't crowd out documents and vice
// versa. Empty groups serialize as empty arrays so the UI can render
// section headers without a per-field nil check.
type SuggestResult struct {
	Documents []DocumentSuggestion `json:"documents"`
	Tags      []ValueSuggestion    `json:"tags"`
	People    []ValueSuggestion    `json:"people"`
	Recent    []RecentSuggestion   `json:"recent"`
}

// DocumentSuggestion is one row in the Documents group. Carries the
// document_id so the UI can navigate straight to the doc detail
// page on enter.
type DocumentSuggestion struct {
	Text       string  `json:"text"`
	DocumentID string  `json:"document_id"`
	Score      float64 `json:"score"`
}

// ValueSuggestion is one row in the Tags or People group.
// Count is the number of docs in the user's permission scope that
// carry this value — the same number the corresponding facet would
// surface, useful as a relevance tiebreaker in the UI.
type ValueSuggestion struct {
	Text  string `json:"text"`
	Count int64  `json:"count"`
}

// RecentSuggestion is one entry from the user's saved-recent ledger.
type RecentSuggestion struct {
	Text string `json:"text"`
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
	// AlertFrequencyCron — optional cron expression. When set, the
	// alert workflow uses it instead of NotifyIntervalMinutes. ADR
	// 0068 §"Cron".
	AlertFrequencyCron    string                  `json:"alert_frequency_cron,omitempty"`
	// WorkflowID — Temporal handle the alert is bound to. Empty
	// when notify=false. Set by the service layer when an alert
	// is started; cleared on stop.
	WorkflowID            string                  `json:"workflow_id,omitempty"`
	// LastMatchDocIDs — diff cursor, written by the alert
	// workflow. Not exposed to API callers in plaintext (the JSON
	// tag is on the response shape; keeping the field on the model
	// for serialization). Empty until the first run lands.
	LastMatchDocIDs       []string                `json:"last_match_doc_ids,omitempty"`
	CreatedAt             time.Time               `json:"created_at"`
	LastRunAt             *time.Time              `json:"last_run_at,omitempty"`
	// Subscribers — embedded on GET responses. Owner is always
	// implicitly subscribed when notify=true; explicit subscribers
	// fan out to a team.
	Subscribers           []SavedSearchSubscriber `json:"subscribers,omitempty"`
	SubscriberCount       int                     `json:"subscriber_count"`
	// ADR 0100 — smart folder fields. Default values (is_smart_folder=
	// false, tree_visibility="private") mean a vanilla saved search
	// continues to behave exactly as before; smart-folder display is
	// opt-in via /promote.
	IsSmartFolder   bool       `json:"is_smart_folder"`
	TreeVisibility  string     `json:"tree_visibility"`             // private | workspace | public
	WorkspaceID     *string    `json:"workspace_id,omitempty"`      // required when tree_visibility = workspace
	Icon            string     `json:"icon"`                        // lucide icon name; default 'sparkles'
	SmartFolderAt   *time.Time `json:"smart_folder_at,omitempty"`
}

// SavedSearchSubscriber is one row in saved_search_subscribers.
// Channels carries the per-user delivery preference; valid values
// mirror the notifications service: "in_app", "email", "digest".
type SavedSearchSubscriber struct {
	UserID        string    `json:"user_id"`
	Channels      []string  `json:"channels"`
	SubscribedBy  string    `json:"subscribed_by"`
	SubscribedAt  time.Time `json:"subscribed_at"`
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
	// migration window so docs indexed before ADR 0083 still match
	// queries from clients that already use the split fields.
	ReadableBy        []string           `json:"readable_by"`
	// ReadableByUsers — direct grants + group memberships expanded
	// to individual user_ids. Owned by the document service's
	// publish-time expansion (ADR 0083 §"Indexer split"). Falls back
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
