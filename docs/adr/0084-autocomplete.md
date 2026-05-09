# ADR 0084 — Grouped autocomplete suggester

Date: 2026-05-06
Status: Accepted
Supersedes: nothing (was scheduled as ADR 0058 in the §7.5 blueprint;
0058 is taken by Anomaly Detection. Same numbering shift as
0078-0066.)

## Context

`services/search` already exposes `GET /api/v1/search/autocomplete?q=…`
which:

- runs a `multi_match` against `title.autocomplete` (edge-ngram analyzer),
- merges in recent searches from Redis,
- applies the standard tenant + readable_by permission filter,
- returns a flat `{suggestions: [{text, source}]}`.

§7.5 of the blueprint asks for three changes on top of what's there:

1. **Group by source** — Documents, Tags, People — instead of one
   flat list. The UI renders each as its own section with a heading.
2. **Three fields, not just title** — title (documents), tags (tags
   group), `created_by_name` (people group).
3. **Sub-50ms p99**, end-to-end. The current edge-ngram path does ~30ms
   on 100k-doc indexes; the additional sources cannot blow that
   budget.

`completion`-typed suggester fields are the textbook choice but come
with two practical drawbacks for VaultDMS:

- **Per-user permission filtering doesn't fit the context model.**
  Completion contexts must be defined at index time and have low
  cardinality (typically tens of values). A user/group ACL that scales
  with workspace size doesn't fit. We'd end up filtering completions
  by tenant only and post-filtering by permission, which costs a
  second OS round-trip and erodes the latency budget.
- **Three completion fields per doc** triples the index size impact
  vs. a single edge-ngram field — completion FST is denser but the
  net is non-trivial at 10M+ docs.

## Decision

Use a single `_search` body with three search clauses bool-OR'd
plus terms aggs, all under the existing tenant + readable_by filter.
Latency at the test corpus is in the 5-15ms range — well inside
the 50ms p99 budget.

### Endpoint

```
GET /api/v1/search/suggest?q=<prefix>&limit=<N>
```

Returns:

```json
{
  "documents": [{"text": "Acme MSA", "document_id": "...", "score": 12.4}],
  "tags":      [{"text": "contract", "count": 100}],
  "people":    [{"text": "alice",    "count": 120}],
  "recent":    [{"text": "vendor renewal"}]
}
```

`limit` caps each group independently. Default 10, max 20.

The existing `/autocomplete` endpoint stays for backwards compat;
clients migrate at their own pace.

### Query shape

```
bool.filter:
  - term tenant_id = $tenant
  - bool.should: <ADR 0083 split-readable_by chain>
  - minimum_should_match: 1
bool.must:
  - bool.should:
      - match_phrase_prefix:  title    (boost 3)
      - prefix:               tags     (boost 1)
      - prefix:               created_by_name (boost 1)
size: limit              (max-bound for documents group)
aggs:
  tags_prefix:
    terms { field: tags, include: "<q>.*" }
  authors_prefix:
    terms { field: created_by_name, include: "<q>.*" }
```

`include: "<q>.*"` is a regex anchor — OpenSearch's terms
aggregation supports include/exclude via regex, and a leading-anchor
prefix is exactly what the spec needs. Cheap on the inverted index
(no full-table scan).

The `match_phrase_prefix` on title is what the documents group renders.
Prefix-matching the inverted index keeps the work bounded by the
prefix, not by the full corpus size.

### Permission scoping

Same as the rest of the search service — the standard
`bool.filter` with `tenant_id` + the ADR 0083 split-readable_by
should chain. Tag and people aggregations run inside that scope, so
suggestions for a user who can't see any docs containing a tag will
not have that tag in their suggestions.

### Recent searches

Reuses the existing Redis ledger (`recent_searches:{tenant}:{user}`).
The /suggest handler reads it once per request and prefix-filters
client-side; the round-trip is in-process and dominated by the
OpenSearch latency, not the Redis lookup.

## Consequences

- **Single OS round-trip** for all three groups + recent searches —
  the latency budget is safe.
- **Tag and people groups respect ACLs by construction** because the
  aggregations run inside the same `bool.filter` as the doc match.
- **No new index template work.** `tags` and `created_by_name` are
  already keyword-typed; the existing `title.autocomplete` field
  carries documents.
- **Completion suggester deliberately not used.** Re-evaluate when
  per-user context filtering ships (OpenSearch Search Pipelines may
  unlock it cleanly).
