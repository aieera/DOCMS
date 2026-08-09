package opensearch

import (
	"encoding/base64"
	"encoding/json"

	"github.com/aieera/sedoc/services/search/internal/model"
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

	// ---- bool query -------------------------------------------------------
	musts := []any{}
	if req.Query != "" {
		// Fields searched per a single typed query. Ordered roughly by
		// signal strength: title > tags > description > OCR'd body >
		// custom metadata > NER entities > author/class/folder.
		// `custom_metadata.*` is a field-wildcard so user-defined
		// metadata fields (whatever shape the tenant adds via the
		// JSON-Schema admin) are included automatically. `lenient:
		// true` makes type mismatches between text/keyword/numeric
		// custom-metadata children non-fatal.
		musts = append(musts, map[string]any{
			"multi_match": map[string]any{
				"query": req.Query,
				"fields": []string{
					"title^3",
					"title.keyword^5",
					// Edge-ngram sub-field — required so filenames
					// the standard analyzer leaves un-split (e.g.
					// "Arabic.pdf" → single token "arabic.pdf") still
					// match prefix-style queries like "arabic". Lower
					// boost than `title` so an exact title hit still
					// outranks a prefix-only one.
					"title.autocomplete^2",
					"tags^2",
					"description^1.5",
					"content^1",            // OCR / extracted text
					"custom_metadata.*^1",  // tenant-defined metadata fields
					"extracted_entities.*", // NER: people, organizations, locations, amounts, dates
					"created_by_name",      // author
					"document_class",       // classification
					"folder_path",          // virtual file-path
				},
				"type":      "best_fields",
				"fuzziness": "AUTO",
				"lenient":   true,
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
		"_source": SourceFields,
	}

	// ---- sort -------------------------------------------------------------
	body["sort"] = buildSort(req.SortBy, req.SortOrder)

	// ---- deep pagination via search_after (Workstream 7) ------------------
	// No `from`: results past OpenSearch's 10k from+size ceiling are reachable.
	// The cursor is the previous page's last hit's sort values; the first page
	// omits it. Requires the deterministic total-ordering sort below (every
	// branch ends with the unique document_id tiebreaker).
	if after := DecodeSearchAfter(req.PageToken); len(after) > 0 {
		body["search_after"] = after
	}

	// ---- highlight --------------------------------------------------------
	if req.Highlight {
		hl := map[string]any{
			"fields": map[string]any{
				"title":   map[string]any{"number_of_fragments": 1, "fragment_size": 200},
				"content": map[string]any{"number_of_fragments": 3, "fragment_size": 150},
			},
			"pre_tags":  []string{"<mark>"},
			"post_tags": []string{"</mark>"},
			// SECURITY: the web client injects these fragments as HTML so
			// the <mark> wrappers render. The default encoder does NOT
			// escape the document's own text — a title like
			// "<img onerror=…>.pdf" would become stored XSS for every
			// searcher. encoder=html makes OpenSearch escape everything
			// except the pre/post tags above.
			"encoder": "html",
		}
		// Track 6 — <mark> only tokens that match WITHOUT fuzziness. The outer
		// query keeps "fuzziness": AUTO for recall (so "contarct" still finds
		// "contract"), but the unified highlighter would otherwise mark fuzzy
		// near-misses too — searching "contract" highlighted "contact" (1 edit
		// away). Re-running the query here with no fuzziness keeps highlights
		// exact while recall is unchanged. Skipped for empty/browse queries.
		if req.Query != "" {
			hl["highlight_query"] = map[string]any{
				"multi_match": map[string]any{
					"query":  req.Query,
					"fields": []string{"title", "content", "tags", "description"},
					"type":   "best_fields",
				},
			}
		}
		body["highlight"] = hl
	}

	// ---- aggregations -----------------------------------------------------
	// ADR 0082 — facet shapes resolved through the FacetSpec registry
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
			// using the legacy FacetSizes map. ADR 0082 prefers the
			// new symbolic names (doc_type, classification, ...).
			if spec, ok := ResolveFacet(name); ok {
				aggs[name] = spec.BuildAgg()
				continue
			}
			sz, ok := FacetSizes[name]
			if !ok {
				// Unknown facet name: DROP it (as the doc comment above promises).
				// The previous fallback used the raw client-supplied name AS the
				// aggregation field, so a caller could aggregate over security
				// fields — facets:["share_tokens"] enumerated anonymous-access
				// tokens, ["readable_by_users"]/["readable_by_groups"] the ACL
				// principals — of documents in the result set (Epic 9 #1).
				// FacetSizes + the ResolveFacet registry are the allowlist.
				continue
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
// with the ADR 0083 split-readable security filter.
func BuildAutocompleteQuery(tenantID, userID string, groupIDs []string, q string, limit int) map[string]any {
	if limit <= 0 || limit > 20 {
		limit = 10
	}
	groups := groupIDs
	if groups == nil {
		groups = []string{}
	}
	groupsWithEveryone := append(append([]string(nil), groups...), "everyone")
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
					map[string]any{
						"bool": map[string]any{
							"should": []any{
								map[string]any{"terms": map[string]any{"readable_by_users": []string{userID}}},
								map[string]any{"terms": map[string]any{"readable_by_groups": groupsWithEveryone}},
								// Legacy bridge — see buildFilters().
								map[string]any{"terms": map[string]any{"readable_by": buildPrincipals(userID, groupIDs)}},
							},
							"minimum_should_match": 1,
						},
					},
				},
			},
		},
		"size":    limit,
		"_source": []string{"document_id", "title"},
	}
}

// ---- internal helpers -----------------------------------------------------

func buildFilters(req *model.SearchRequest) []any {
	// ADR 0083 — split readable_by into per-shape clauses so the
	// matched access path is identifiable post-hoc, AND so a user
	// newly-added to a group matches docs without waiting for a
	// reindex (the group_id is on the doc; the user_id isn't).
	//
	// bool.should + minimum_should_match=1 means "at least one of
	// these access paths must match". Wrapped under bool.filter so
	// the match runs in the non-scoring context — same as the legacy
	// shape, no scoring impact.
	//
	// §7.3 SHARE-TOKEN ISOLATION: when the request is from an
	// unauthenticated share-link follower (UserID empty/sentinel
	// AND ShareToken set), the should chain becomes share-only.
	// We do NOT inject the user/group/everyone clauses, otherwise
	// the auto-injected "everyone" group would leak the full
	// "tenant-wide visible" corpus to anyone with any valid
	// share token. The spec calls this out: "External share token:
	// separate matching logic."
	// Mandatory security filters — never omitted. tenant_id is a
	// hard term filter (not part of the should chain) so a
	// misconfigured shoulds list can't cause cross-tenant leaks.
	filters := []any{
		map[string]any{"term": map[string]any{"tenant_id": req.TenantID}},
		map[string]any{
			"bool": map[string]any{
				"should":               aclShoulds(req),
				"minimum_should_match": 1,
			},
		},
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
	if len(f.CreatedByName) > 0 {
		filters = append(filters, map[string]any{"terms": map[string]any{"created_by_name": f.CreatedByName}})
	}
	if len(f.RegionPin) > 0 {
		filters = append(filters, map[string]any{"terms": map[string]any{"region_pin": f.RegionPin}})
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

	// Hygiene: drop orphaned/empty index records — a document with no
	// extracted content AND zero bytes is a placeholder shell (it
	// surfaced as an "Untitled document" (0 B) hit with no snippet).
	// Require at least one of: indexed content, or a positive size, so
	// real documents (including binary files with no text layer) still
	// match while empty shells are excluded.
	filters = append(filters, map[string]any{
		"bool": map[string]any{
			"should": []any{
				map[string]any{"exists": map[string]any{"field": "content"}},
				map[string]any{"range": map[string]any{"size_bytes": map[string]any{"gt": 0}}},
			},
			"minimum_should_match": 1,
		},
	})

	return filters
}

// isAnonymousPrincipal returns true when the request lacks a real
// authenticated user identity. The gateway routes a public
// share-link URL with one of these sentinels in X-User-ID:
//   - "" (empty): preferred shape, no header at all
//   - "anonymous": legacy alias kept for in-flight integrations
//
// Any other value means a logged-in user is making the request, in
// which case the share-token clause stacks ON TOP of their normal
// access (logged-in user following a share link sees the union).
func isAnonymousPrincipal(userID string) bool {
	return userID == "" || userID == "anonymous"
}

// aclShoulds builds the readable_by / share-token should-chain (the per-request
// ACL access paths). §7.3: an unauthenticated share-link follower is scoped to
// their token only (no user/group/everyone clauses), otherwise the "everyone"
// clause would leak the tenant-wide corpus. Shared by the main search filter and
// BuildHydrateByIDsQuery.
func aclShoulds(req *model.SearchRequest) []any {
	if req.ShareToken != "" && isAnonymousPrincipal(req.UserID) {
		return []any{
			map[string]any{"terms": map[string]any{"share_tokens": []string{req.ShareToken}}},
		}
	}
	groups := req.GroupIDs
	if groups == nil {
		groups = []string{}
	}
	groupsWithEveryone := append(append([]string(nil), groups...), "everyone")
	shoulds := []any{
		map[string]any{"terms": map[string]any{"readable_by_users": []string{req.UserID}}},
		map[string]any{"terms": map[string]any{"readable_by_groups": groupsWithEveryone}},
		// Migration bridge — docs indexed before ADR 0083 only carry the mixed
		// `readable_by` field. Drop this clause once the backfill is complete.
		map[string]any{"terms": map[string]any{"readable_by": buildPrincipals(req.UserID, req.GroupIDs)}},
	}
	if req.ShareToken != "" {
		// Logged-in user following a share link — union of normal access + token.
		shoulds = append(shoulds, map[string]any{
			"terms": map[string]any{"share_tokens": []string{req.ShareToken}},
		})
	}
	return shoulds
}

// BuildHydrateByIDsQuery returns an OpenSearch body that selects, from the given
// document ids, ONLY those that (a) the request's principals may currently view
// and (b) still satisfy the request's own filters — and returns their full
// _source so the caller can render them.
//
// It is the single hydration step behind the dense-vector (semantic / hybrid)
// path, and it carries THREE guarantees the vector path cannot provide itself:
//
//  1. ACL re-verification. The Qdrant payload's readable_by is only rewritten by
//     the intelligence service on re-embed (content change), so a permission
//     REVOKE leaves it stale and a raw semantic hit can name a document the
//     caller may no longer view (Epic 9 #3).
//  2. Filter parity. buildFilters is the SAME filter set the lexical branch
//     applies (workspace/folder/class/lifecycle/tags/mime/author/region/date/
//     size/custom-metadata + the empty-shell hygiene clause). Without it, a
//     vector hit survived filters the lexical branch correctly removed — an
//     impossible date range still returned the semantic half of the result set.
//  3. Metadata hydration. `_source: SourceFields` is exactly what the lexical
//     branch returns, so a fused row carries title / size / dates / snippet /
//     lifecycle_state / created_by_name rather than a document_id-only stub
//     (which the UI rendered as a dead "Untitled document" card).
//
// An id absent from the response is one of: deleted, no longer readable, or
// filtered out — the caller MUST drop it from both the results and the count.
func BuildHydrateByIDsQuery(req *model.SearchRequest, ids []string) map[string]any {
	filters := append(buildFilters(req),
		map[string]any{"ids": map[string]any{"values": ids}})
	return map[string]any{
		"_source": SourceFields,
		"size":    len(ids),
		"query": map[string]any{
			"bool": map[string]any{"filter": filters},
		},
	}
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

// buildSort always ends with the unique document_id tiebreaker so the sort is a
// TOTAL order — a hard requirement for search_after (a non-unique final sort key
// can skip or repeat rows across pages). document_id is a keyword field in the
// index mapping, so it's safely sortable.
func buildSort(sortBy, sortOrder string) []any {
	order := "desc"
	if sortOrder == "asc" {
		order = "asc"
	}
	const tiebreaker = "document_id"
	switch sortBy {
	case "created_at", "updated_at", "size_bytes":
		return []any{
			map[string]any{sortBy: map[string]any{"order": order}},
			map[string]any{tiebreaker: map[string]any{"order": "asc"}},
		}
	case "title":
		return []any{
			map[string]any{"title.keyword": map[string]any{"order": order}},
			map[string]any{tiebreaker: map[string]any{"order": "asc"}},
		}
	default: // "relevance"
		return []any{
			"_score",
			map[string]any{tiebreaker: map[string]any{"order": "asc"}},
		}
	}
}

// QueryOnlyBody extracts just the `query` clause from a full
// search body so it can be reused as the body of a _count request.
// Aggregations / sort / size / from / highlight all dropped — _count
// rejects them.
func QueryOnlyBody(searchBody map[string]any) map[string]any {
	if q, ok := searchBody["query"]; ok {
		return map[string]any{"query": q}
	}
	return map[string]any{"query": map[string]any{"match_all": map[string]any{}}}
}

// StripAggs returns a shallow copy of the search body with `aggs`
// removed. Used when the count gate decides the query is too large
// to aggregate over.
func StripAggs(searchBody map[string]any) map[string]any {
	out := make(map[string]any, len(searchBody))
	for k, v := range searchBody {
		if k == "aggs" {
			continue
		}
		out[k] = v
	}
	return out
}

// DecodeSearchAfter reverses EncodeSearchAfter: an opaque base64(JSON-array)
// cursor → the OpenSearch `search_after` sort values. Empty / malformed / legacy
// (the old base64 integer offset) tokens decode to nil, i.e. "first page", so a
// rollout from the from+size cursor degrades gracefully instead of erroring.
func DecodeSearchAfter(token string) []any {
	if token == "" {
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return nil
	}
	var after []any
	if err := json.Unmarshal(b, &after); err != nil {
		return nil // old integer-offset token or garbage → start over
	}
	return after
}

// EncodeSearchAfter packs a hit's sort values into an opaque cursor. nil/empty
// sort → "" (no further pages).
func EncodeSearchAfter(sort []any) string {
	if len(sort) == 0 {
		return ""
	}
	b, err := json.Marshal(sort)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}
