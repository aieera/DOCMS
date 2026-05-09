// Package opensearch — facet registry + aggregation shape builder.
// ADR 0082 covers the design.
package opensearch

import (
	"strings"
)

// FacetKind enumerates the aggregation shapes we support.
type FacetKind int

const (
	// Terms is a categorical bucket aggregation.
	Terms FacetKind = iota
	// DateHistogram buckets a date field by calendar interval.
	DateHistogram
	// Range buckets a numeric field by a fixed list of [from, to)
	// ranges. Used for size + amount-extracted facets.
	Range
)

// RangeBucket defines one bucket for a Range facet.
// `From` is inclusive, `To` is exclusive. Both pointers — nil means
// "open-ended on this side" (matching ES semantics).
type RangeBucket struct {
	Key  string
	From *int64
	To   *int64
}

// FacetSpec describes one registered facet. Name is what the API
// caller passes in `facet=...`; Field is the OpenSearch mapping
// field name. Sizes/intervals/ranges are kind-specific knobs.
type FacetSpec struct {
	Name             string
	Kind             FacetKind
	Field            string
	Size             int           // Terms only
	CalendarInterval string        // DateHistogram only ("day"|"month"|"year")
	Ranges           []RangeBucket // Range only
}

// CustomFacetPrefix marks a facet name as a JSONB custom-metadata
// terms aggregation: `custom:contract_value` → terms on
// `custom_metadata.contract_value`. The field must be `keyword`-typed
// in the index mapping; the indexer enforces this for fields
// registered as filterable.
const CustomFacetPrefix = "custom:"

// Built-in facet registry. Each entry maps a stable API name to the
// OpenSearch aggregation it produces. Additions here surface in the
// admin and public APIs without further glue.
//
// §7.2 calls out: doc type, tag, author, date range, classification,
// region_pin, lifecycle_state, content_type. We expose them under
// the names the spec uses and keep the underlying field names (which
// match the IndexDocument) as implementation detail.
var BuiltInFacets = map[string]FacetSpec{
	"doc_type":        {Name: "doc_type", Kind: Terms, Field: "mime_type", Size: 20},
	"tag":             {Name: "tag", Kind: Terms, Field: "tags", Size: 50},
	"author":          {Name: "author", Kind: Terms, Field: "created_by_name", Size: 20},
	"classification":  {Name: "classification", Kind: Terms, Field: "document_class", Size: 20},
	"region_pin":      {Name: "region_pin", Kind: Terms, Field: "region_pin", Size: 10},
	"lifecycle_state": {Name: "lifecycle_state", Kind: Terms, Field: "lifecycle_state", Size: 10},
	"content_type":    {Name: "content_type", Kind: Terms, Field: "mime_type", Size: 20},
	"workspace_id":    {Name: "workspace_id", Kind: Terms, Field: "workspace_id", Size: 50},
	// Date facet — month buckets give a usable timeline overview at
	// most tenant sizes. Day/year are easy to expose as a knob if
	// needed; not in the §7.2 spec for this release.
	"created_at": {
		Name: "created_at", Kind: DateHistogram,
		Field: "created_at", CalendarInterval: "month",
	},
	// Size facet — five buckets matched to common Office / scanned-PDF
	// distributions. Open-ended top bucket catches the long tail.
	"size": {
		Name: "size", Kind: Range, Field: "size_bytes",
		Ranges: []RangeBucket{
			{Key: "0-100KB", To: int64Ptr(100_000)},
			{Key: "100KB-1MB", From: int64Ptr(100_000), To: int64Ptr(1_000_000)},
			{Key: "1MB-10MB", From: int64Ptr(1_000_000), To: int64Ptr(10_000_000)},
			{Key: "10MB-100MB", From: int64Ptr(10_000_000), To: int64Ptr(100_000_000)},
			{Key: "100MB+", From: int64Ptr(100_000_000)},
		},
	},
}

func int64Ptr(v int64) *int64 { return &v }

// ResolveFacet returns the FacetSpec for a caller-supplied name.
// `custom:foo` → a Terms spec on `custom_metadata.foo`. Built-ins
// resolved by direct map lookup. Unknown names return false so the
// caller can ignore + log rather than 4xx (a stale client requesting
// a deprecated facet shouldn't break the search).
func ResolveFacet(name string) (FacetSpec, bool) {
	if strings.HasPrefix(name, CustomFacetPrefix) {
		field := strings.TrimPrefix(name, CustomFacetPrefix)
		if field == "" {
			return FacetSpec{}, false
		}
		return FacetSpec{
			Name:  name,
			Kind:  Terms,
			Field: "custom_metadata." + field,
			Size:  20,
		}, true
	}
	spec, ok := BuiltInFacets[name]
	return spec, ok
}

// BuildAgg renders a FacetSpec into the OpenSearch aggregation JSON.
// The caller wraps the result under `body["aggs"][name] = …`.
func (f FacetSpec) BuildAgg() map[string]any {
	switch f.Kind {
	case Terms:
		size := f.Size
		if size <= 0 {
			size = 20
		}
		return map[string]any{
			"terms": map[string]any{"field": f.Field, "size": size},
		}
	case DateHistogram:
		interval := f.CalendarInterval
		if interval == "" {
			interval = "month"
		}
		return map[string]any{
			"date_histogram": map[string]any{
				"field":             f.Field,
				"calendar_interval": interval,
				"min_doc_count":     1,
			},
		}
	case Range:
		ranges := make([]map[string]any, 0, len(f.Ranges))
		for _, r := range f.Ranges {
			b := map[string]any{"key": r.Key}
			if r.From != nil {
				b["from"] = *r.From
			}
			if r.To != nil {
				b["to"] = *r.To
			}
			ranges = append(ranges, b)
		}
		return map[string]any{
			"range": map[string]any{
				"field":  f.Field,
				"ranges": ranges,
			},
		}
	}
	return nil
}
