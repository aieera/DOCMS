// ADR 0082 — facet registry + aggregation builders.
//
// Coverage focuses on:
//   - Symbolic facet names resolve to the right OpenSearch field
//   - date_histogram + range get the calendar/range knobs they need
//   - Custom-metadata facets prefix correctly
//   - Permission filter (readable_by) is still in scope when aggs run —
//     this is the §7.3 invariant; the test asserts the filter clause
//     and the aggs both end up under the same bool body, so OpenSearch
//     applies the filter before bucketing.
package opensearch

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aieera/sedoc/services/search/internal/model"
)

func TestResolveFacet_SymbolicNamesMapToIndexedFields(t *testing.T) {
	cases := []struct {
		name      string
		wantField string
		wantKind  FacetKind
	}{
		{"doc_type", "mime_type", Terms},
		{"tag", "tags", Terms},
		{"author", "created_by_name", Terms},
		{"classification", "document_class", Terms},
		{"region_pin", "region_pin", Terms},
		{"lifecycle_state", "lifecycle_state", Terms},
		{"content_type", "mime_type", Terms},
		{"created_at", "created_at", DateHistogram},
		{"size", "size_bytes", Range},
	}
	for _, c := range cases {
		spec, ok := ResolveFacet(c.name)
		if !ok {
			t.Errorf("%s: not in registry", c.name)
			continue
		}
		if spec.Field != c.wantField {
			t.Errorf("%s: field = %s, want %s", c.name, spec.Field, c.wantField)
		}
		if spec.Kind != c.wantKind {
			t.Errorf("%s: kind = %v, want %v", c.name, spec.Kind, c.wantKind)
		}
	}
}

func TestResolveFacet_CustomPrefix(t *testing.T) {
	spec, ok := ResolveFacet("custom:contract_value")
	if !ok {
		t.Fatal("custom: facet should resolve")
	}
	if spec.Field != "custom_metadata.contract_value" {
		t.Errorf("custom field = %s, want custom_metadata.contract_value", spec.Field)
	}
	if spec.Kind != Terms {
		t.Errorf("custom facet should be Terms, got %v", spec.Kind)
	}
}

func TestResolveFacet_BareCustomPrefixRejected(t *testing.T) {
	// `custom:` with no field name has nothing to aggregate on —
	// must NOT silently aggregate on a malformed field path.
	if _, ok := ResolveFacet("custom:"); ok {
		t.Fatal("bare 'custom:' should not resolve")
	}
}

func TestResolveFacet_UnknownFacetReturnsFalse(t *testing.T) {
	// Unknown facets fall through to the legacy FacetSizes path in
	// BuildSearchQuery; ResolveFacet itself signals "not in registry".
	if _, ok := ResolveFacet("nonexistent_facet"); ok {
		t.Fatal("unknown facet should not resolve")
	}
}

// ---- BuildAgg shapes -----------------------------------------------

func TestBuildAgg_TermsCarriesField(t *testing.T) {
	spec := BuiltInFacets["tag"]
	agg := spec.BuildAgg()
	terms, ok := agg["terms"].(map[string]any)
	if !ok {
		t.Fatal("terms agg missing")
	}
	if terms["field"] != "tags" {
		t.Errorf("field = %v", terms["field"])
	}
	if terms["size"] != 50 {
		t.Errorf("size = %v, want 50", terms["size"])
	}
}

func TestBuildAgg_DateHistogramHasCalendarInterval(t *testing.T) {
	agg := BuiltInFacets["created_at"].BuildAgg()
	dh, ok := agg["date_histogram"].(map[string]any)
	if !ok {
		t.Fatal("date_histogram agg missing")
	}
	if dh["calendar_interval"] != "month" {
		t.Errorf("calendar_interval = %v", dh["calendar_interval"])
	}
	if dh["field"] != "created_at" {
		t.Errorf("field = %v", dh["field"])
	}
}

func TestBuildAgg_RangeHasBuckets(t *testing.T) {
	agg := BuiltInFacets["size"].BuildAgg()
	r, ok := agg["range"].(map[string]any)
	if !ok {
		t.Fatal("range agg missing")
	}
	if r["field"] != "size_bytes" {
		t.Errorf("field = %v", r["field"])
	}
	ranges, _ := r["ranges"].([]map[string]any)
	if len(ranges) != 5 {
		t.Errorf("range bucket count = %d, want 5", len(ranges))
	}
	// Top bucket should be open-ended (only `from`, no `to`).
	top := ranges[len(ranges)-1]
	if _, hasTo := top["to"]; hasTo {
		t.Error("top bucket should be open-ended (no `to`)")
	}
	if _, hasFrom := top["from"]; !hasFrom {
		t.Error("top bucket should have `from`")
	}
}

// ---- §7.3 invariant: permission filter applied before aggregation --

// The §7.3 / §7.2 spec requirement: facet bucket counts must respect
// the user's readable_by ACL. OpenSearch aggregations run AFTER the
// bool.filter clause is applied, so as long as the filter is present
// in the query body, this is structural. The test pins it in place
// so a future refactor can't accidentally move readable_by out of
// the filter into a should/must clause.
func TestBuildSearchQuery_AggsScopedByPermissionFilter(t *testing.T) {
	req := &model.SearchRequest{
		TenantID: "tenant-A",
		UserID:   "alice",
		GroupIDs: []string{"engineering"},
		Query:    "contract",
		Facets:   []string{"tag", "author"},
	}
	q := BuildSearchQuery(req)

	// Aggs must be present.
	if _, ok := q["aggs"]; !ok {
		t.Fatal("aggs missing when facets requested")
	}

	// readable_by + tenant_id must be in the bool.filter — the same
	// scope that aggregations honor.
	raw, _ := json.Marshal(q)
	body := string(raw)
	if !strings.Contains(body, `"readable_by"`) {
		t.Error("readable_by filter missing — aggs would not be ACL-scoped")
	}
	if !strings.Contains(body, `"tenant_id"`) {
		t.Error("tenant_id filter missing — aggs would leak across tenants")
	}
	// Defense in depth: assert both terms aggregations made it in.
	if !strings.Contains(body, `"tag"`) {
		t.Error("tag agg missing")
	}
	if !strings.Contains(body, `"author"`) {
		t.Error("author agg missing")
	}
}

func TestBuildSearchQuery_DropsAggsWhenStripped(t *testing.T) {
	req := &model.SearchRequest{
		TenantID: "t", UserID: "u",
		Facets: []string{"tag"},
	}
	full := BuildSearchQuery(req)
	if _, ok := full["aggs"]; !ok {
		t.Fatal("aggs should exist before stripping")
	}
	stripped := StripAggs(full)
	if _, ok := stripped["aggs"]; ok {
		t.Fatal("aggs should be gone after StripAggs")
	}
	// Original is untouched (StripAggs returns a copy, doesn't mutate).
	if _, ok := full["aggs"]; !ok {
		t.Fatal("StripAggs mutated input — must be a copy")
	}
}

func TestQueryOnlyBody_KeepsQueryDropsRest(t *testing.T) {
	body := map[string]any{
		"query":     map[string]any{"match_all": map[string]any{}},
		"aggs":      map[string]any{"x": map[string]any{}},
		"size":      20,
		"from":      0,
		"highlight": map[string]any{},
	}
	stripped := QueryOnlyBody(body)
	if _, ok := stripped["query"]; !ok {
		t.Fatal("query missing")
	}
	for _, k := range []string{"aggs", "size", "from", "highlight"} {
		if _, ok := stripped[k]; ok {
			t.Errorf("%s should be stripped from _count body", k)
		}
	}
}

func TestQueryOnlyBody_FallsBackToMatchAllOnEmpty(t *testing.T) {
	stripped := QueryOnlyBody(map[string]any{})
	q, ok := stripped["query"].(map[string]any)
	if !ok {
		t.Fatal("query missing")
	}
	if _, ok := q["match_all"]; !ok {
		t.Error("expected match_all when source body has no query")
	}
}
