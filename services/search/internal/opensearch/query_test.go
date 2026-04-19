package opensearch

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/vaultdms/vaultdms/services/search/internal/model"
)

func TestBuildSearchQuery_AlwaysIncludesReadableByFilter(t *testing.T) {
	req := &model.SearchRequest{
		TenantID: "tenant-1",
		UserID:   "user-1",
		GroupIDs: []string{"group-a", "group-b"},
		Query:    "test query",
	}
	q := BuildSearchQuery(req)
	raw, _ := json.Marshal(q)

	// readable_by must appear in the filter clause.
	if !containsStr(string(raw), "readable_by") {
		t.Fatal("readable_by filter missing from query")
	}
	if !containsStr(string(raw), "tenant_id") {
		t.Fatal("tenant_id filter missing from query")
	}
}

func TestBuildSearchQuery_PermissionFilterCannotBeOmitted(t *testing.T) {
	// Even with zero filters and empty query, the security filters must be present.
	req := &model.SearchRequest{
		TenantID: "t",
		UserID:   "u",
	}
	q := BuildSearchQuery(req)
	boolQ := q["query"].(map[string]any)["bool"].(map[string]any)
	filters := boolQ["filter"].([]any)

	if len(filters) < 2 {
		t.Fatalf("expected at least 2 mandatory filters (tenant + readable_by), got %d", len(filters))
	}
}

func TestBuildSearchQuery_FacetsIncluded(t *testing.T) {
	req := &model.SearchRequest{
		TenantID: "t",
		UserID:   "u",
		Facets:   []string{"document_class", "tags"},
	}
	q := BuildSearchQuery(req)
	aggs, ok := q["aggs"].(map[string]any)
	if !ok {
		t.Fatal("aggregations not included")
	}
	if _, ok := aggs["document_class"]; !ok {
		t.Fatal("document_class facet missing")
	}
	if _, ok := aggs["tags"]; !ok {
		t.Fatal("tags facet missing")
	}
}

func TestBuildSearchQuery_AllFilters(t *testing.T) {
	after := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	before := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	minSize := int64(100)
	maxSize := int64(10000)
	hasContent := true

	req := &model.SearchRequest{
		TenantID: "t",
		UserID:   "u",
		GroupIDs: []string{"g1"},
		Query:    "revenue",
		Filters: model.SearchFilters{
			WorkspaceID:    "ws-1",
			FolderID:       "f-1",
			DocumentClass:  []string{"report"},
			LifecycleState: []string{"active"},
			Tags:           []string{"finance"},
			CreatedAfter:   &after,
			CreatedBefore:  &before,
			MimeType:       []string{"application/pdf"},
			SizeMinBytes:   &minSize,
			SizeMaxBytes:   &maxSize,
			CreatedBy:      "bob",
			HasContent:     &hasContent,
		},
		Highlight: true,
	}
	q := BuildSearchQuery(req)
	raw, _ := json.MarshalIndent(q, "", "  ")
	s := string(raw)

	for _, expected := range []string{
		"workspace_id", "folder_id", "document_class", "lifecycle_state",
		"tags", "created_at", "mime_type", "size_bytes", "created_by",
		"readable_by", "tenant_id", "revenue", "mark",
	} {
		if !containsStr(s, expected) {
			t.Errorf("query missing expected field/value: %s", expected)
		}
	}
}

func TestBuildSearchQuery_EmptyQueryUsesMatchAll(t *testing.T) {
	req := &model.SearchRequest{TenantID: "t", UserID: "u"}
	q := BuildSearchQuery(req)
	boolQ := q["query"].(map[string]any)["bool"].(map[string]any)
	musts := boolQ["must"].([]any)
	first := musts[0].(map[string]any)
	if _, ok := first["match_all"]; !ok {
		t.Fatal("empty query should produce match_all")
	}
}

func TestBuildSearchQuery_SortOptions(t *testing.T) {
	cases := []struct {
		sortBy string
		expect string
	}{
		{"relevance", "_score"},
		{"created_at", "created_at"},
		{"title", "title.keyword"},
		{"size_bytes", "size_bytes"},
	}
	for _, tc := range cases {
		q := BuildSearchQuery(&model.SearchRequest{
			TenantID: "t", UserID: "u",
			SortBy: tc.sortBy, SortOrder: "desc",
		})
		raw, _ := json.Marshal(q["sort"])
		if !containsStr(string(raw), tc.expect) {
			t.Errorf("sort_by=%s: expected %s in sort, got %s", tc.sortBy, tc.expect, string(raw))
		}
	}
}

func TestPageTokenRoundtrip(t *testing.T) {
	for _, n := range []int{0, 20, 100, 500} {
		token := EncodePageToken(n)
		got := DecodePageTokenInt(token)
		if n == 0 && token != "" {
			t.Fatalf("0 should encode to empty token")
		}
		if n > 0 && got != n {
			t.Fatalf("roundtrip failed: encoded %d, decoded %d", n, got)
		}
	}
}

func TestBuildAutocompleteQuery_HasSecurityFilter(t *testing.T) {
	q := BuildAutocompleteQuery("t1", "u1", []string{"g1"}, "quar", 10)
	raw, _ := json.Marshal(q)
	if !containsStr(string(raw), "readable_by") {
		t.Fatal("autocomplete query missing readable_by filter")
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && searchStr(s, sub))
}

func searchStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
