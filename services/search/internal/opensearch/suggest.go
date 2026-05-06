// ADR 0067 — grouped autocomplete suggester.
//
// One _search body that produces three groups in a single round-trip:
//   - documents — match_phrase_prefix on title.autocomplete
//   - tags      — terms agg on `tags` keyword field, prefix-filtered
//   - people    — terms agg on `created_by_name`, prefix-filtered
//
// Permission scope: the same `tenant_id` + ADR 0066 split-readable_by
// filter that the main /search uses, so suggestions always honor the
// user's ACL. Tag and people aggregations run inside that scope, so
// values that only appear on docs the user can't read are absent.
//
// Sub-50ms p99 (the §7.5 SLA) is achievable here because:
//   - All three sub-queries are inverted-index lookups bounded by
//     the prefix, not the corpus size.
//   - Aggregations run after filter, on a doc-count typically much
//     smaller than the full tenant corpus once readable_by narrows
//     the candidate set.
//   - One round-trip, not three — the OS coordinator parallelizes
//     internally.
package opensearch

import (
	"regexp"

	"github.com/vaultdms/vaultdms/services/search/internal/model"
)

// MaxSuggestLimit caps a single group at this — the UI shows ~10
// per group typically; 20 is the budgeted ceiling so a chatty
// client can't ask for 1000.
const MaxSuggestLimit = 20

// DefaultSuggestLimit when the caller doesn't specify.
const DefaultSuggestLimit = 10

// BuildSuggestQuery produces the OS body for /search/suggest. The
// caller passes the prefix `q` and the per-group cap `limit`; a
// pre-built model.SearchRequest with TenantID/UserID/GroupIDs/
// ShareToken supplies the permission context.
func BuildSuggestQuery(req *model.SearchRequest, q string, limit int) map[string]any {
	if limit <= 0 {
		limit = DefaultSuggestLimit
	}
	if limit > MaxSuggestLimit {
		limit = MaxSuggestLimit
	}

	// Documents group — match_phrase_prefix on title with a
	// reasonable boost. tags + created_by_name kept in the should
	// chain so a user typing "alice" gets docs alice authored even
	// if the title doesn't say "alice".
	must := []any{
		map[string]any{
			"bool": map[string]any{
				"should": []any{
					map[string]any{
						"match_phrase_prefix": map[string]any{
							"title": map[string]any{"query": q, "boost": 3.0},
						},
					},
					map[string]any{
						"prefix": map[string]any{
							"tags": map[string]any{"value": q, "boost": 1.0},
						},
					},
					map[string]any{
						"prefix": map[string]any{
							"created_by_name": map[string]any{"value": q, "boost": 1.0},
						},
					},
				},
				"minimum_should_match": 1,
			},
		},
	}

	// Pull more rows than the caller asked for so the title-dedupe
	// in service.Suggest has material to work with — a corpus with
	// 40 docs all titled "Contract A" plus 40 titled "Contract B"
	// would otherwise see only "Contract A" in the top-10 results.
	// 5x is empirically generous; bigger is wasted bytes over the
	// wire.
	internalSize := limit * 5
	if internalSize < 50 {
		internalSize = 50
	}

	body := map[string]any{
		"query": map[string]any{
			"bool": map[string]any{
				"must":   must,
				"filter": buildFilters(req),
			},
		},
		"size": internalSize,
		"_source": []string{"document_id", "title"},
		"aggs": map[string]any{
			"tags_prefix": map[string]any{
				"terms": map[string]any{
					"field":   "tags",
					"size":    limit,
					"include": prefixToRegex(q),
				},
			},
			"authors_prefix": map[string]any{
				"terms": map[string]any{
					"field":   "created_by_name",
					"size":    limit,
					"include": prefixToRegex(q),
				},
			},
		},
	}

	return body
}

// prefixToRegex turns a plain prefix into a regex anchor that
// OpenSearch's terms-agg `include` parameter accepts. Special chars
// in the prefix are escaped so a user typing `c++` doesn't blow up
// the regex compiler.
//
// OpenSearch's Lucene regex doesn't honor `(?i)`, so we match
// case-sensitively. Tags are conventionally lowercase; the caller
// passes the raw `q` and we don't lowercase here so the people
// group can match a display-name's natural casing. Frontend can
// lowercase before sending if the tenant's tag taxonomy demands it.
//
// Returns ".*" (match everything) on empty q so a bare /suggest
// without a query still surfaces the top tags + people. The
// documents group still requires a non-empty q because
// match_phrase_prefix needs something to anchor on.
func prefixToRegex(q string) string {
	if q == "" {
		return ".*"
	}
	return regexp.QuoteMeta(q) + ".*"
}
