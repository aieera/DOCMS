// BuildHydrateByIDsQuery is the single hydration step behind the
// semantic / hybrid path. It has to carry three things at once; drop any
// one and a defect the tester filed comes back:
//
//   - _source (else vector rows render as blank "Untitled document" cards)
//   - the ACL should-chain (else a revoked document keeps surfacing)
//   - the request's own filters (else an impossible filter still returns
//     the vector half of the result set)
package opensearch

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aieera/sedoc/services/search/internal/model"
)

func hydrateReq() *model.SearchRequest {
	return &model.SearchRequest{
		TenantID: "tenant-A",
		UserID:   "alice",
		GroupIDs: []string{"engineering"},
		Query:    "invoice",
	}
}

func TestBuildHydrateByIDsQuery_RequestsSourceFields(t *testing.T) {
	q := BuildHydrateByIDsQuery(hydrateReq(), []string{"doc-1", "doc-2"})

	src, ok := q["_source"].([]string)
	if !ok {
		t.Fatalf("_source must be the SourceFields list, got %T (%v)", q["_source"], q["_source"])
	}
	for _, want := range []string{"title", "size_bytes", "created_at", "lifecycle_state", "created_by_name", "content_snippet"} {
		found := false
		for _, f := range src {
			if f == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("_source missing %q — vector hits would render blank", want)
		}
	}
	if q["size"] != 2 {
		t.Errorf("size = %v, want 2 (one slot per candidate)", q["size"])
	}
}

func TestBuildHydrateByIDsQuery_KeepsACLAndTenantScope(t *testing.T) {
	body := must(BuildHydrateByIDsQuery(hydrateReq(), []string{"doc-1"}))
	for _, clause := range []string{
		`"tenant_id"`,
		`"readable_by_users"`,
		`"readable_by_groups"`,
		`"readable_by"`,
		`"minimum_should_match":1`,
		`"ids"`,
	} {
		if !strings.Contains(body, clause) {
			t.Errorf("hydration query missing %s", clause)
		}
	}
}

// The regression: the by-ids query used to apply tenant + ACL only, so
// the vector branch survived filters the lexical branch had removed.
func TestBuildHydrateByIDsQuery_AppliesRequestFilters(t *testing.T) {
	req := hydrateReq()
	after := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	before := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	req.Filters.CreatedAfter = &after
	req.Filters.CreatedBefore = &before
	req.Filters.WorkspaceID = "ws-7"
	req.Filters.Tags = []string{"contract"}
	req.Filters.LifecycleState = []string{"active"}

	body := must(BuildHydrateByIDsQuery(req, []string{"doc-1"}))
	for _, want := range []string{
		"2026-12-31", "2020-01-01", "ws-7", "contract", "active",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("hydration query dropped filter value %q — the vector branch would ignore it", want)
		}
	}
}

// A share-link follower must stay scoped to their token on the hydration
// query too; the "everyone" clause would otherwise leak the tenant-wide
// corpus through the semantic path (§7.3).
func TestBuildHydrateByIDsQuery_ShareTokenScopeIsNotWidened(t *testing.T) {
	req := &model.SearchRequest{TenantID: "t", UserID: "", ShareToken: "tok-abc"}
	body := must(BuildHydrateByIDsQuery(req, []string{"doc-1"}))
	if !strings.Contains(body, `"tok-abc"`) {
		t.Error("share token clause missing")
	}
	if strings.Contains(body, `"everyone"`) {
		t.Error("anonymous share follower must NOT get the everyone clause")
	}
}

func TestBuildHydrateByIDsQuery_IDsAreCarriedVerbatim(t *testing.T) {
	q := BuildHydrateByIDsQuery(hydrateReq(), []string{"doc-1", "doc-2", "doc-3"})
	raw, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"doc-1", "doc-2", "doc-3"} {
		if !strings.Contains(string(raw), id) {
			t.Errorf("candidate %s missing from the ids clause", id)
		}
	}
}
