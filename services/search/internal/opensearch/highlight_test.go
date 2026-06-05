package opensearch

import (
	"encoding/json"
	"testing"

	"github.com/aieera/sedoc/services/search/internal/model"
)

// Track 6 — the unified highlighter must <mark> only tokens that match the
// user's query EXACTLY, not the fuzzy near-misses the outer AUTO-fuzziness
// query admits (searching "contract" was highlighting "contact", 1 edit away).
// The structural fix: the highlight block carries a highlight_query whose
// multi_match sets NO fuzziness, while the outer query keeps AUTO for recall.
func TestBuildSearchQuery_HighlightQueryHasNoFuzziness(t *testing.T) {
	req := &model.SearchRequest{
		TenantID:  "t",
		UserID:    "u",
		Query:     "contract termination",
		Highlight: true,
	}
	q := BuildSearchQuery(req)

	hl, ok := q["highlight"].(map[string]any)
	if !ok {
		t.Fatal("highlight block missing")
	}
	hq, ok := hl["highlight_query"].(map[string]any)
	if !ok {
		t.Fatal("highlight_query missing — the highlighter would mark fuzzy near-misses")
	}
	mm, ok := hq["multi_match"].(map[string]any)
	if !ok {
		t.Fatal("highlight_query.multi_match missing")
	}
	if _, has := mm["fuzziness"]; has {
		t.Error("highlight_query must NOT set fuzziness — that's exactly what marks near-misses")
	}

	// The outer query must STILL carry AUTO fuzziness so recall is unchanged
	// (typo tolerance: "contarct" still finds "contract").
	raw, _ := json.Marshal(q["query"])
	if !containsStr(string(raw), "AUTO") {
		t.Error("outer query lost AUTO fuzziness — recall would regress")
	}
}

// A browse / empty-query highlight must NOT attach a highlight_query — an empty
// multi_match is meaningless and error-prone.
func TestBuildSearchQuery_EmptyQueryNoHighlightQuery(t *testing.T) {
	req := &model.SearchRequest{
		TenantID:  "t",
		UserID:    "u",
		Query:     "",
		Highlight: true,
	}
	q := BuildSearchQuery(req)
	hl, ok := q["highlight"].(map[string]any)
	if !ok {
		t.Fatal("highlight block missing")
	}
	if _, has := hl["highlight_query"]; has {
		t.Error("empty/browse query should not attach a highlight_query")
	}
}
