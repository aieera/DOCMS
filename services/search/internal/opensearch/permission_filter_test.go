// ADR 0066 — split-readable_by query construction.
//
// The §7.3 invariant: every search query MUST scope by tenant_id
// AND match at least one of (readable_by_users, readable_by_groups,
// share_tokens, legacy readable_by). The tests pin the body shape
// so a future refactor can't accidentally widen tenant scope or
// drop the access check.
package opensearch

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vaultdms/vaultdms/services/search/internal/model"
)

func TestBuildSearchQuery_ContainsAllAccessClauses(t *testing.T) {
	req := &model.SearchRequest{
		TenantID: "tenant-A",
		UserID:   "alice",
		GroupIDs: []string{"engineering", "legal"},
		Query:    "anything",
	}
	q := BuildSearchQuery(req)
	raw, _ := json.Marshal(q)
	body := string(raw)

	for _, clause := range []string{
		`"readable_by_users"`,
		`"readable_by_groups"`,
		`"readable_by"`, // legacy bridge
		`"tenant_id"`,
		`"minimum_should_match":1`,
	} {
		if !strings.Contains(body, clause) {
			t.Errorf("missing clause %s in query body", clause)
		}
	}
}

func TestBuildSearchQuery_ShareTokenClauseAppearsOnlyWhenPresent(t *testing.T) {
	noToken := &model.SearchRequest{TenantID: "t", UserID: "u"}
	q1 := BuildSearchQuery(noToken)
	if strings.Contains(must(q1), `"share_tokens"`) {
		t.Error("share_tokens clause must NOT appear when ShareToken is empty")
	}

	withToken := &model.SearchRequest{TenantID: "t", UserID: "u", ShareToken: "tok-abc"}
	q2 := BuildSearchQuery(withToken)
	if !strings.Contains(must(q2), `"share_tokens"`) {
		t.Error("share_tokens clause must appear when ShareToken is set")
	}
	if !strings.Contains(must(q2), `"tok-abc"`) {
		t.Error("share token value missing from query body")
	}
}

func TestBuildSearchQuery_TenantIDIsHardFilterNotInsideShould(t *testing.T) {
	// Privacy guarantee: tenant_id must be a top-level term filter,
	// NOT part of the bool.should chain. Otherwise a misconfigured
	// shoulds list could (in theory) widen tenant scope.
	req := &model.SearchRequest{
		TenantID: "tenant-A",
		UserID:   "alice",
		GroupIDs: []string{"engineering"},
	}
	q := BuildSearchQuery(req)
	boolQ := q["query"].(map[string]any)["bool"].(map[string]any)
	filters := boolQ["filter"].([]any)

	// First filter clause MUST be the tenant_id term.
	first, ok := filters[0].(map[string]any)
	if !ok {
		t.Fatal("first filter clause is not a map")
	}
	if _, hasTerm := first["term"]; !hasTerm {
		t.Fatalf("first filter must be a term clause; was %v", first)
	}
	termInner := first["term"].(map[string]any)
	if _, hasTenant := termInner["tenant_id"]; !hasTenant {
		t.Fatalf("first filter must scope tenant_id; got %v", termInner)
	}
}

func TestBuildSearchQuery_AccessShouldUsesMinShouldMatchOne(t *testing.T) {
	req := &model.SearchRequest{TenantID: "t", UserID: "u", GroupIDs: []string{"g1"}}
	q := BuildSearchQuery(req)
	boolQ := q["query"].(map[string]any)["bool"].(map[string]any)
	filters := boolQ["filter"].([]any)

	// Find the bool.should clause among filters.
	var found bool
	for _, f := range filters {
		fm, ok := f.(map[string]any)
		if !ok {
			continue
		}
		bm, ok := fm["bool"].(map[string]any)
		if !ok {
			continue
		}
		// minimum_should_match is the load-bearing knob — without it,
		// a should clause is just a scoring nudge and the doc would
		// match regardless of access.
		mss, ok := bm["minimum_should_match"]
		if !ok {
			continue
		}
		if mss != 1 {
			t.Errorf("minimum_should_match=%v want 1", mss)
		}
		found = true
		break
	}
	if !found {
		t.Fatal("bool.should access clause not found inside filter")
	}
}

func TestBuildSearchQuery_GroupsAlwaysIncludeEveryone(t *testing.T) {
	// "everyone" is the tenant-wide-public marker; every read query
	// must include it in the readable_by_groups list, otherwise
	// docs without per-user grants become unreachable.
	req := &model.SearchRequest{TenantID: "t", UserID: "u"}
	q := BuildSearchQuery(req)
	if !strings.Contains(must(q), `"everyone"`) {
		t.Error("everyone must appear in groups list — public docs would be unreachable otherwise")
	}
}

func TestBuildAutocompleteQuery_HasSplitFields(t *testing.T) {
	q := BuildAutocompleteQuery("t", "u", []string{"g1"}, "test", 10)
	body := must(q)
	for _, clause := range []string{
		`"readable_by_users"`,
		`"readable_by_groups"`,
		`"readable_by"`,
		`"tenant_id"`,
	} {
		if !strings.Contains(body, clause) {
			t.Errorf("autocomplete query missing clause %s", clause)
		}
	}
}

// must is a small helper to JSON-marshal a query body for substring
// assertions. Panics on encode error so the failing test surfaces
// the bug location, not a json error.
func must(q map[string]any) string {
	b, err := json.Marshal(q)
	if err != nil {
		panic(err)
	}
	return string(b)
}
