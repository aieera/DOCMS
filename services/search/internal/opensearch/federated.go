// ADR 0069 — federated (cross-tenant) query builder.
//
// Different from BuildSearchQuery in TWO ways and TWO ways only:
//   1. NO `tenant_id` term filter — we span every tenant.
//   2. NO `readable_by_*` should chain — platform admin sees the
//      raw index, not a per-user ACL view.
// The rest (text matching, filters, size, source fields) mirrors
// the regular path so a federated search behaves predictably.
//
// Caller MUST verify platform-admin perm + audit BEFORE invoking
// this; the function itself has no notion of who's calling.
package opensearch

import (
	"github.com/aieera/sedoc/services/search/internal/model"
)

// BuildFederatedSearchQuery produces the OS body for the cross-tenant
// path. Everything in `req.Filters` is honored, but `req.TenantID`
// is IGNORED — by design.
func BuildFederatedSearchQuery(req *model.SearchRequest) map[string]any {
	pageSize := req.PageSize
	if pageSize <= 0 || pageSize > MaxPageSize {
		pageSize = DefaultPageSize
	}

	musts := []any{}
	if req.Query != "" {
		// Mirror the per-tenant field list so federated and per-tenant
		// callers see consistent recall. See query.go for rationale.
		musts = append(musts, map[string]any{
			"multi_match": map[string]any{
				"query": req.Query,
				"fields": []string{
					"title^3",
					"title.keyword^5",
					"title.autocomplete^2",
					"tags^2",
					"description^1.5",
					"content^1",
					"custom_metadata.*^1",
					"extracted_entities.*",
					"created_by_name",
					"document_class",
					"folder_path",
				},
				"type":      "best_fields",
				"fuzziness": "AUTO",
				"lenient":   true,
			},
		})
	}
	if len(musts) == 0 {
		musts = []any{map[string]any{"match_all": map[string]any{}}}
	}

	// Filter list MIRRORS the per-tenant path's filter helpers but
	// deliberately starts EMPTY (no tenant_id, no readable_by). We
	// re-implement the field-by-field filter expansion inline so
	// nobody can accidentally route through buildFilters() and
	// re-introduce the security clauses that this whole endpoint
	// is opting out of.
	filters := []any{}
	f := req.Filters
	if f.WorkspaceID != "" {
		filters = append(filters, map[string]any{"term": map[string]any{"workspace_id": f.WorkspaceID}})
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

	// Group hits by tenant_id so the response can render
	// "matches in 3 tenants" without the caller bucketing it
	// in client code.
	body := map[string]any{
		"query": map[string]any{
			"bool": map[string]any{
				"must":   musts,
				"filter": filters,
			},
		},
		"size":    pageSize,
		"_source": append(SourceFields, "tenant_id"), // include tenant_id in hit payload
		"aggs": map[string]any{
			"by_tenant": map[string]any{
				"terms": map[string]any{
					"field": "tenant_id",
					"size":  100,
				},
			},
		},
	}
	return body
}

// FederatedIndexPattern is the OpenSearch index pattern the
// federated query searches across — every tenant index. Kept as a
// constant so the handler doesn't string-format an index name from
// caller-supplied data.
const FederatedIndexPattern = "dms-documents-*"
