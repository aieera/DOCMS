// ADR 0069 — federated query body invariants.
//
// The §7.7 critical guarantee: BuildFederatedSearchQuery omits
// tenant_id and readable_by clauses by construction. These tests
// pin that property in place so a future refactor can't silently
// re-introduce them via routing through the regular buildFilters
// path.
package opensearch

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vaultdms/vaultdms/services/search/internal/model"
)

func TestBuildFederatedSearchQuery_OmitsTenantAndReadableByFilters(t *testing.T) {
	// The §7.7 critical guarantee: federated query MUST NOT add a
	// tenant_id filter or any readable_by filter clause. Aggregating
	// or sourcing tenant_id is fine (we want to know which tenant
	// each hit came from); FILTERING on it would defeat the whole
	// point of the cross-tenant path.
	req := &model.SearchRequest{
		// Even when TenantID is set on the request struct (which a
		// caller might do by accident), the federated query MUST
		// NOT add a tenant_id filter clause.
		TenantID: "should-be-ignored",
		UserID:   "alice",
		GroupIDs: []string{"engineering"},
		Query:    "anything",
	}
	body := BuildFederatedSearchQuery(req)

	boolQ := body["query"].(map[string]any)["bool"].(map[string]any)
	filters, _ := boolQ["filter"].([]any)
	for _, f := range filters {
		fm, _ := f.(map[string]any)
		raw, _ := json.Marshal(fm)
		s := string(raw)
		// Each filter clause must not target the security fields.
		for _, forbidden := range []string{
			`"tenant_id"`,
			`"readable_by"`,
			`"readable_by_users"`,
			`"readable_by_groups"`,
		} {
			if strings.Contains(s, forbidden) {
				t.Errorf("filter clause contains %s — must not (cross-tenant by design): %s", forbidden, s)
			}
		}
	}
	// Also assert there's no `must` clause filtering on these
	// fields — `must` is the lexical-match list, not the filter
	// list, but a refactor could put a term match on tenant_id
	// here and we'd want the test to catch it.
	musts, _ := boolQ["must"].([]any)
	for _, m := range musts {
		mm, _ := m.(map[string]any)
		raw, _ := json.Marshal(mm)
		s := string(raw)
		for _, forbidden := range []string{
			`"readable_by"`, `"readable_by_users"`, `"readable_by_groups"`,
		} {
			if strings.Contains(s, forbidden) {
				t.Errorf("must clause contains %s", forbidden)
			}
		}
	}
}

func TestBuildFederatedSearchQuery_HasByTenantAgg(t *testing.T) {
	// The handler groups results_by_tenant from the response; the
	// terms agg on tenant_id surfaces a quick "how many tenants
	// matched" number even when the hit slice is paginated.
	req := &model.SearchRequest{Query: "x"}
	body := BuildFederatedSearchQuery(req)
	aggs, ok := body["aggs"].(map[string]any)
	if !ok {
		t.Fatal("aggs missing")
	}
	by, ok := aggs["by_tenant"].(map[string]any)
	if !ok {
		t.Fatal("by_tenant agg missing")
	}
	terms, ok := by["terms"].(map[string]any)
	if !ok {
		t.Fatal("by_tenant terms agg missing")
	}
	if terms["field"] != "tenant_id" {
		t.Errorf("by_tenant agg field=%v want tenant_id", terms["field"])
	}
}

func TestBuildFederatedSearchQuery_PassesThroughStandardFilters(t *testing.T) {
	// Filter knobs that DON'T leak across tenants (tags, mime,
	// classification, lifecycle) MUST still be honored — federated
	// search is "all tenants, narrowed by the same business
	// filters".
	req := &model.SearchRequest{
		Query: "x",
		Filters: model.SearchFilters{
			Tags:           []string{"contract"},
			MimeType:       []string{"application/pdf"},
			DocumentClass:  []string{"msa"},
			LifecycleState: []string{"active"},
		},
	}
	body := BuildFederatedSearchQuery(req)
	raw, _ := json.Marshal(body)
	s := string(raw)
	for _, want := range []string{`"tags"`, `"mime_type"`, `"document_class"`, `"lifecycle_state"`} {
		if !strings.Contains(s, want) {
			t.Errorf("federated body missing filter %s", want)
		}
	}
}

func TestBuildFederatedSearchQuery_NoQueryStillBuildsValidBody(t *testing.T) {
	// Empty query → match_all. The endpoint should be usable as a
	// "list everything in tenant X" probe by setting the
	// workspace_id filter without a text query.
	req := &model.SearchRequest{}
	body := BuildFederatedSearchQuery(req)
	raw, _ := json.Marshal(body)
	if !strings.Contains(string(raw), `"match_all"`) {
		t.Error("empty query body must use match_all")
	}
}
