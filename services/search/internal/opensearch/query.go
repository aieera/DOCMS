package opensearch

import (
	"encoding/base64"
	"fmt"
	"strconv"

	"github.com/vaultdms/vaultdms/services/search/internal/model"
)

// DefaultPageSize caps result sets.
const DefaultPageSize = 20

// MaxPageSize prevents excessive memory use.
const MaxPageSize = 100

// SourceFields are the fields returned in _source (no full content).
var SourceFields = []string{
	"document_id", "title", "description", "document_class",
	"lifecycle_state", "workspace_id", "folder_id", "tags",
	"created_by", "created_by_name", "created_at", "updated_at",
	"size_bytes", "mime_type", "has_thumbnail", "version_count",
	"content_snippet",
}

// FacetSizes controls how many buckets each facet returns.
var FacetSizes = map[string]int{
	"document_class":  20,
	"tags":            50,
	"created_by_name": 20,
	"lifecycle_state": 10,
	"workspace_id":    20,
	"mime_type":       20,
}

// BuildSearchQuery translates a model.SearchRequest into the OpenSearch
// JSON body. The readable_by filter is ALWAYS injected — there is no code
// path that omits it.
func BuildSearchQuery(req *model.SearchRequest) map[string]any {
	pageSize := req.PageSize
	if pageSize <= 0 || pageSize > MaxPageSize {
		pageSize = DefaultPageSize
	}
	from := decodePageToken(req.PageToken)

	// ---- bool query -------------------------------------------------------
	musts := []any{}
	if req.Query != "" {
		musts = append(musts, map[string]any{
			"multi_match": map[string]any{
				"query":     req.Query,
				"fields":    []string{"title^3", "title.keyword^5", "description^1.5", "content^1", "tags^2"},
				"type":      "best_fields",
				"fuzziness": "AUTO",
			},
		})
	}

	filters := buildFilters(req)

	boolQ := map[string]any{"filter": filters}
	if len(musts) > 0 {
		boolQ["must"] = musts
	} else {
		boolQ["must"] = []any{map[string]any{"match_all": map[string]any{}}}
	}

	body := map[string]any{
		"query":   map[string]any{"bool": boolQ},
		"size":    pageSize,
		"from":    from,
		"_source": SourceFields,
	}

	// ---- sort -------------------------------------------------------------
	body["sort"] = buildSort(req.SortBy, req.SortOrder)

	// ---- highlight --------------------------------------------------------
	if req.Highlight {
		body["highlight"] = map[string]any{
			"fields": map[string]any{
				"title":   map[string]any{"number_of_fragments": 1, "fragment_size": 200},
				"content": map[string]any{"number_of_fragments": 3, "fragment_size": 150},
			},
			"pre_tags":  []string{"<mark>"},
			"post_tags": []string{"</mark>"},
		}
	}

	// ---- aggregations -----------------------------------------------------
	// ADR 0065 — facet shapes resolved through the FacetSpec registry
	// so terms / date_histogram / range all work uniformly. Unknown
	// facet names are dropped silently rather than 4xx'd; a stale
	// client requesting a deprecated facet should still get a working
	// search.
	if len(req.Facets) > 0 {
		aggs := map[string]any{}
		for _, name := range req.Facets {
			// Backwards-compat: legacy callers passed raw OpenSearch
			// field names (mime_type, document_class, ...). When a name
			// isn't in the registry, fall back to a terms aggregation
			// using the legacy FacetSizes map. ADR 0065 prefers the
			// new symbolic names (doc_type, classification, ...).
			if spec, ok := ResolveFacet(name); ok {
				aggs[name] = spec.BuildAgg()
				continue
			}
			sz, ok := FacetSizes[name]
			if !ok {
				sz = 20
			}
			aggs[name] = map[string]any{
				"terms": map[string]any{"field": name, "size": sz},
			}
		}
		body["aggs"] = aggs
	}

	if req.Explain {
		body["explain"] = true
	}

	return body
}

// BuildAutocompleteQuery returns a lightweight title.autocomplete query
// with the readable_by security filter.
func BuildAutocompleteQuery(tenantID, userID string, groupIDs []string, q string, limit int) map[string]any {
	if limit <= 0 || limit > 20 {
		limit = 10
	}
	principals := buildPrincipals(userID, groupIDs)
	return map[string]any{
		"query": map[string]any{
			"bool": map[string]any{
				"must": []any{
					map[string]any{
						"multi_match": map[string]any{
							"query":  q,
							"fields": []string{"title.autocomplete^2", "title^1"},
							"type":   "best_fields",
						},
					},
				},
				"filter": []any{
					map[string]any{"term": map[string]any{"tenant_id": tenantID}},
					map[string]any{"terms": map[string]any{"readable_by": principals}},
				},
			},
		},
		"size":    limit,
		"_source": []string{"document_id", "title"},
	}
}

// ---- internal helpers -----------------------------------------------------

func buildFilters(req *model.SearchRequest) []any {
	principals := buildPrincipals(req.UserID, req.GroupIDs)

	// Mandatory security filters — never omitted.
	filters := []any{
		map[string]any{"term": map[string]any{"tenant_id": req.TenantID}},
		map[string]any{"terms": map[string]any{"readable_by": principals}},
	}

	f := req.Filters
	if f.WorkspaceID != "" {
		filters = append(filters, map[string]any{"term": map[string]any{"workspace_id": f.WorkspaceID}})
	}
	if f.FolderID != "" {
		filters = append(filters, map[string]any{"term": map[string]any{"folder_id": f.FolderID}})
	}
	if len(f.DocumentClass) > 0 {
		filters = append(filters, map[string]any{"terms": map[string]any{"document_class": f.DocumentClass}})
	}
	if len(f.LifecycleState) > 0 {
		filters = append(filters, map[string]any{"terms": map[string]any{"lifecycle_state": f.LifecycleState}})
	}
	if len(f.Tags) > 0 {
		filters = append(filters, map[string]any{"terms": map[string]any{"tags": f.Tags}})
	}
	if len(f.MimeType) > 0 {
		filters = append(filters, map[string]any{"terms": map[string]any{"mime_type": f.MimeType}})
	}
	if f.CreatedBy != "" {
		filters = append(filters, map[string]any{"term": map[string]any{"created_by": f.CreatedBy}})
	}

	rangeQ := map[string]any{}
	if f.CreatedAfter != nil {
		rangeQ["gte"] = f.CreatedAfter.Format("2006-01-02T15:04:05Z")
	}
	if f.CreatedBefore != nil {
		rangeQ["lte"] = f.CreatedBefore.Format("2006-01-02T15:04:05Z")
	}
	if len(rangeQ) > 0 {
		filters = append(filters, map[string]any{"range": map[string]any{"created_at": rangeQ}})
	}

	sizeRange := map[string]any{}
	if f.SizeMinBytes != nil {
		sizeRange["gte"] = *f.SizeMinBytes
	}
	if f.SizeMaxBytes != nil {
		sizeRange["lte"] = *f.SizeMaxBytes
	}
	if len(sizeRange) > 0 {
		filters = append(filters, map[string]any{"range": map[string]any{"size_bytes": sizeRange}})
	}

	if f.HasContent != nil && *f.HasContent {
		filters = append(filters, map[string]any{"exists": map[string]any{"field": "content"}})
	}

	for k, v := range f.CustomMetadata {
		filters = append(filters, map[string]any{"term": map[string]any{"custom_metadata." + k: v}})
	}

	return filters
}

func buildPrincipals(userID string, groupIDs []string) []string {
	out := make([]string, 0, len(groupIDs)+2)
	if userID != "" {
		out = append(out, userID)
	}
	out = append(out, groupIDs...)
	out = append(out, "everyone")
	return out
}

func buildSort(sortBy, sortOrder string) []any {
	order := "desc"
	if sortOrder == "asc" {
		order = "asc"
	}
	switch sortBy {
	case "created_at", "updated_at", "size_bytes":
		return []any{
			map[string]any{sortBy: map[string]any{"order": order}},
			"_score",
		}
	case "title":
		return []any{
			map[string]any{"title.keyword": map[string]any{"order": order}},
			"_score",
		}
	default: // "relevance"
		return []any{"_score"}
	}
}

// DecodePageTokenInt is the exported counterpart used by the service layer.
func DecodePageTokenInt(token string) int { return decodePageToken(token) }

func decodePageToken(token string) int {
	if token == "" {
		return 0
	}
	b, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(string(b))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// EncodePageToken encodes the next "from" offset.
func EncodePageToken(from int) string {
	if from <= 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d", from)))
}
