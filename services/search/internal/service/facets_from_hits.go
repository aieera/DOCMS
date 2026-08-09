package service

import (
	"sort"
	"strings"

	"github.com/aieera/sedoc/services/search/internal/model"
	"github.com/aieera/sedoc/services/search/internal/opensearch"
)

// facetsFromHits derives facet buckets from the documents actually being
// returned, rather than from OpenSearch aggregations.
//
// Why this exists: aggregations are computed by OpenSearch over the BM25
// match set. Qdrant has no aggregation layer, so on a semantic response
// there were no buckets at all, and on a hybrid response the buckets
// described only the lexical half — the sidebar reported "3" while the
// header reported the fused total. Counting over the hit set makes the
// two agree by construction.
//
// Only Terms-shaped facets are derivable this way; date_histogram and
// range facets (created_at, size) need OpenSearch's bucketing and are
// skipped rather than approximated. Unknown facet names are dropped, the
// same allowlist behaviour BuildSearchQuery applies (Epic 9 #1 — a facet
// name is never used as a raw field name).
//
// Returns nil when nothing could be derived, so the response omits
// `facets` instead of carrying an empty object.
func facetsFromHits(hits []opensearch.RawHit, names []string) map[string][]model.FacetBucket {
	if len(names) == 0 || len(hits) == 0 {
		return nil
	}
	out := make(map[string][]model.FacetBucket, len(names))
	for _, name := range names {
		field, size, ok := facetTermsField(name)
		if !ok {
			continue
		}
		counts := make(map[string]int64)
		for _, h := range hits {
			for _, v := range facetValues(h.Source, field) {
				counts[v]++
			}
		}
		if len(counts) == 0 {
			continue
		}
		buckets := make([]model.FacetBucket, 0, len(counts))
		for v, c := range counts {
			buckets = append(buckets, model.FacetBucket{Value: v, Count: c})
		}
		// Count desc, then value asc — OpenSearch's terms ordering, and
		// deterministic (Go map iteration is not).
		sort.Slice(buckets, func(i, j int) bool {
			if buckets[i].Count != buckets[j].Count {
				return buckets[i].Count > buckets[j].Count
			}
			return buckets[i].Value < buckets[j].Value
		})
		if size > 0 && len(buckets) > size {
			buckets = buckets[:size]
		}
		out[name] = buckets
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// facetTermsField resolves a caller-supplied facet name to the indexed
// field it aggregates, mirroring BuildSearchQuery: the ADR 0082 registry
// first, then the legacy raw-field-name map. ok=false for unknown names
// and for non-Terms shapes.
func facetTermsField(name string) (string, int, bool) {
	if spec, ok := opensearch.ResolveFacet(name); ok {
		if spec.Kind != opensearch.Terms {
			return "", 0, false
		}
		return spec.Field, spec.Size, true
	}
	if sz, ok := opensearch.FacetSizes[name]; ok {
		return name, sz, true
	}
	return "", 0, false
}

// facetValues pulls the string value(s) of a (possibly dotted, e.g.
// `custom_metadata.contract_value`) field out of an OpenSearch _source.
// Multi-valued fields such as `tags` contribute one count per value,
// matching terms-aggregation semantics. Empty strings are skipped — a
// blank bucket is noise, not a facet.
func facetValues(src map[string]any, field string) []string {
	var cur any = src
	for _, part := range strings.Split(field, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = m[part]
		if !ok {
			return nil
		}
	}
	switch v := cur.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
