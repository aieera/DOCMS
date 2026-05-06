# ADR 0065 — Faceted search aggregations + caching

Date: 2026-05-06
Status: Accepted
Supersedes: nothing (was scheduled as ADR 0056 in the §7.2 blueprint;
0056 is taken by Translation Pipeline. Same numbering shift as
0061/0062/0063/0064.)

## Context

`services/search` already exposes POST /api/v1/search with a `facets`
list that drives a single-shape `terms` aggregation per facet. The
§7.2 blueprint asks for four things on top of what's there:

1. **More facet shapes** — date_histogram for `created_at`,
   range for `size_bytes`, plus terms for the remaining categorical
   fields (`region_pin`, `mime_type`, `document_class`, etc.) and a
   way to register arbitrary indexed JSONB custom-metadata fields as
   facets.
2. **Performance gates** — large result sets (> 1M hits inside the
   permission scope) must skip aggregation entirely; common facet
   shapes should be Redis-cached for 60 s so repeated dashboard
   refreshes don't keep hammering OpenSearch.
3. **A GET endpoint** that follows the spec's URL syntax
   `?q=X&facet=tag,author&filter=tag:contract&filter=author:alice`
   so search results can be deep-linked from anywhere.
4. **Frontend sidebar** with URL-bound state so a search-with-filters
   is bookmarkable and "Save as" resurfaces the existing saved-searches
   API.

The permission filter is already correct — `tenant_id` and
`readable_by` are mandatory `bool.filter` clauses, and OpenSearch
aggregations run inside that scope automatically. No change needed
to satisfy "permission filter applied before aggregation" — the
property is structural.

## Decision

### FacetSpec registry

A small registry maps a facet name to its OpenSearch aggregation
shape. The query builder consults the registry rather than
hard-coding shape per field.

```
FacetSpec{
  Name: "doc_type",   Kind: Terms, Field: "mime_type", Size: 20
}
FacetSpec{
  Name: "created_at", Kind: DateHistogram, Field: "created_at",
                      CalendarInterval: "month"
}
FacetSpec{
  Name: "size",       Kind: Range, Field: "size_bytes",
                      Ranges: [{to: 100KB}, {from: 100KB, to: 1MB},
                               {from: 1MB, to: 10MB}, {from: 10MB}]
}
```

Built-ins (per §7.2):

| Facet name        | Kind            | Field                |
|-------------------|-----------------|----------------------|
| `doc_type`        | terms           | `mime_type`          |
| `tag`             | terms           | `tags`               |
| `author`          | terms           | `created_by_name`    |
| `classification`  | terms           | `document_class`     |
| `region_pin`      | terms           | `region_pin`         |
| `lifecycle_state` | terms           | `lifecycle_state`    |
| `content_type`    | terms           | `mime_type`          |
| `created_at`      | date_histogram  | `created_at`         |
| `size`            | range           | `size_bytes`         |

Custom-metadata facets are addressed via the `custom:{field}`
prefix — e.g. `facet=custom:contract_value` runs a terms aggregation
on `custom_metadata.contract_value`. The field must be present in
the OpenSearch mapping as `keyword` (the indexer enforces that for
indexed JSONB fields).

### Skip-when-large

OpenSearch returns total hits cheaply (`track_total_hits=true`); the
expensive part of a 1M+ hit query is the aggregation. Two-step:

1. Run a **count** query first with the full filter scope (including
   `readable_by`). If `> 1M`, drop `aggs` from the search body and
   return `facets={}` to the caller.
2. Cache the "this filter shape has too many hits" decision in
   Redis for 60 s so the next page request doesn't re-count.

Cost: one extra round-trip to OpenSearch for queries that DO have
aggs requested — cheap relative to a large aggregation. We do
not add the count query to the no-facet path.

### Facet caching

Redis hash keyed by
`facets:{tenant}:{sha256(query + filters + facets + principals)}`,
60 s TTL. Cache value is the marshaled `map[string][]FacetBucket`.
The cache key includes principals (user_id + group_ids) so two users
in the same tenant with different group memberships don't share
buckets — a fresh user joining a group must see facet counts that
include their newly-readable docs. Cache is invalidated implicitly
by the 60 s TTL (no proactive busting) — facet counts on a hot
dashboard are inherently slightly-stale anyway.

### GET /api/v1/search

Spec syntax: `?q=X&facet=tag,author&filter=tag:contract&filter=author:alice`.

Decoded as:

| Param | Maps to |
|-------|---------|
| `q` | `Query` |
| `facet` (comma-separated) | `Facets` list |
| `filter` (repeated, `key:value` form) | per-key collection into `Filters.{Tags,DocumentClass,...}` |
| `sort_by`, `sort_order`, `page_size`, `page_token` | as POST |
| `mode` | `lexical`/`semantic`/`hybrid` |

Filter keys mirror the field names (`tag`, `author`, `classification`,
`mime_type`, etc.); date/size ranges use bracket notation
`?filter=created_at:[2026-01-01,2026-04-30]` and
`?filter=size:[100000,]`.

GET and POST share the service-layer entry point; only the parsing
differs. POST stays the documented shape for programmatic clients.

## Consequences

- **Counts respect ACLs by construction.** `readable_by` is a
  `bool.filter` clause, and OpenSearch aggregations are scoped to
  the filter. Tested in `aggs_permission_test.go`.
- **Two OpenSearch round-trips** when facets are requested for the
  first time after cache miss — count + search-with-aggs. The
  second-and-onward requests within 60s read from Redis.
- **Facet bucket counts are slightly stale** within the 60s window.
  This is the right trade for a dashboard pattern; users querying
  for a specific document don't typically request facets.
- **Custom-metadata facets need `keyword` mapping**. The doc indexer
  already enforces this for fields registered as filterable; this
  ADR doesn't add new mapping requirements.
