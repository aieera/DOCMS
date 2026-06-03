// ADR 0084 — suggest query construction tests.
//
// Pins the §7.5 invariants in place:
//   - permission filter (tenant_id + ADR 0083 split-readable_by) is
//     present on every body — aggregations honor the user's ACL by
//     construction
//   - all three sources (title, tags, created_by_name) appear in the
//     should chain
//   - terms aggs use a regex-anchored include so the prefix work
//     stays bounded
//   - prefixToRegex escapes special chars so a user typing `c++`
//     doesn't blow up the regex compiler
package opensearch

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aieera/sedoc/services/search/internal/model"
)

func TestBuildSuggestQuery_HasAllThreeSources(t *testing.T) {
	req := &model.SearchRequest{
		TenantID: "t1", UserID: "u1", GroupIDs: []string{"engineering"},
	}
	body := BuildSuggestQuery(req, "cont", 10)
	raw, _ := json.Marshal(body)
	s := string(raw)
	for _, clause := range []string{
		`"match_phrase_prefix"`, `"title"`,
		`"prefix"`,
		`"tags"`, `"created_by_name"`,
	} {
		if !strings.Contains(s, clause) {
			t.Errorf("missing clause %s in suggest body", clause)
		}
	}
}

func TestBuildSuggestQuery_HasBothTermsAggs(t *testing.T) {
	req := &model.SearchRequest{TenantID: "t1", UserID: "u1"}
	body := BuildSuggestQuery(req, "cont", 10)
	aggs, ok := body["aggs"].(map[string]any)
	if !ok {
		t.Fatal("aggs missing")
	}
	if _, ok := aggs["tags_prefix"]; !ok {
		t.Error("tags_prefix agg missing")
	}
	if _, ok := aggs["authors_prefix"]; !ok {
		t.Error("authors_prefix agg missing")
	}
}

func TestBuildSuggestQuery_TermsAggIncludeIsAnchoredRegex(t *testing.T) {
	req := &model.SearchRequest{TenantID: "t1", UserID: "u1"}
	body := BuildSuggestQuery(req, "alic", 10)
	tags := body["aggs"].(map[string]any)["tags_prefix"].(map[string]any)["terms"].(map[string]any)
	include, _ := tags["include"].(string)
	if !strings.HasPrefix(include, "alic") || !strings.HasSuffix(include, ".*") {
		t.Errorf("tags include=%q want anchored prefix regex", include)
	}
}

func TestBuildSuggestQuery_AppliesPermissionFilter(t *testing.T) {
	// §7.3 invariant: the permission filter is a HARD bool.filter
	// clause. Aggregations run inside that filter, so values from
	// docs the user can't read never surface.
	req := &model.SearchRequest{
		TenantID: "tenant-A",
		UserID:   "alice",
		GroupIDs: []string{"engineering"},
	}
	body := BuildSuggestQuery(req, "cont", 10)
	raw, _ := json.Marshal(body)
	s := string(raw)
	for _, clause := range []string{
		`"tenant_id"`,
		`"readable_by_users"`,
		`"readable_by_groups"`,
	} {
		if !strings.Contains(s, clause) {
			t.Errorf("permission clause %s missing — aggregations would leak across tenants/ACLs", clause)
		}
	}
}

func TestBuildSuggestQuery_LimitClampedToMax(t *testing.T) {
	req := &model.SearchRequest{TenantID: "t1", UserID: "u1"}
	body := BuildSuggestQuery(req, "x", 999)
	// internalSize is limit*5 (or min 50), and limit is clamped to
	// MaxSuggestLimit=20 first. So internalSize = 100, not 4995.
	if size, ok := body["size"].(int); !ok || size > MaxSuggestLimit*5 {
		t.Errorf("size=%v exceeds clamp budget", body["size"])
	}
}

func TestPrefixToRegex_EscapesSpecialChars(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"alice", `alice.*`},
		{"c++", `c\+\+.*`},
		{"foo.bar", `foo\.bar.*`},
		{"", ".*"}, // empty prefix → match everything (used for empty-q discovery)
	}
	for _, c := range cases {
		got := prefixToRegex(c.in)
		if got != c.want {
			t.Errorf("prefixToRegex(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestBuildSuggestQuery_DefaultsLimitWhenZero(t *testing.T) {
	req := &model.SearchRequest{TenantID: "t1", UserID: "u1"}
	body := BuildSuggestQuery(req, "x", 0)
	// Should fall back to DefaultSuggestLimit=10, internal size = 50.
	if size, ok := body["size"].(int); !ok || size < DefaultSuggestLimit {
		t.Errorf("size=%v want >= %d", body["size"], DefaultSuggestLimit)
	}
}
