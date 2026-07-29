// ADR 0082 — URL parser tests for the GET /search shape.
package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/pkg/auth"
)

func TestParseURL_BasicQueryAndFacets(t *testing.T) {
	// Identity is read from the request context (populated by the
	// IdentityHeadersHTTP / SessionAuth middleware in prod), not from
	// raw headers — so seed the context the way the middleware would.
	tid, uid := uuid.New(), uuid.New()
	r := httptest.NewRequest("GET",
		"/api/v1/search?q=contract&facet=tag,author", nil)
	r = r.WithContext(auth.WithUser(auth.SetTenantID(r.Context(), tid),
		auth.UserInfo{ID: uid, TenantID: tid}))
	req := parseSearchRequestFromURL(r)

	if req.TenantID != tid.String() || req.UserID != uid.String() {
		t.Errorf("identity: tenant=%s user=%s", req.TenantID, req.UserID)
	}
	if req.Query != "contract" {
		t.Errorf("query=%q want 'contract'", req.Query)
	}
	if len(req.Facets) != 2 || req.Facets[0] != "tag" || req.Facets[1] != "author" {
		t.Errorf("facets=%v want [tag author]", req.Facets)
	}
}

func TestParseURL_RepeatingFilterParams(t *testing.T) {
	r := httptest.NewRequest("GET",
		"/api/v1/search?filter=tag:contract&filter=tag:legal&filter=author:alice", nil)
	r.Header.Set("X-Auth-Tenant-ID", "t1")
	r.Header.Set("X-User-ID", "u1")
	req := parseSearchRequestFromURL(r)

	if len(req.Filters.Tags) != 2 ||
		req.Filters.Tags[0] != "contract" || req.Filters.Tags[1] != "legal" {
		t.Errorf("tags=%v want [contract legal]", req.Filters.Tags)
	}
	if len(req.Filters.CreatedByName) != 1 || req.Filters.CreatedByName[0] != "alice" {
		t.Errorf("authors=%v want [alice]", req.Filters.CreatedByName)
	}
}

func TestParseURL_DateRangeBracket(t *testing.T) {
	r := httptest.NewRequest("GET",
		"/api/v1/search?filter=created_at:[2026-01-01,2026-04-30]", nil)
	r.Header.Set("X-Auth-Tenant-ID", "t1")
	r.Header.Set("X-User-ID", "u1")
	req := parseSearchRequestFromURL(r)

	if req.Filters.CreatedAfter == nil || req.Filters.CreatedAfter.Year() != 2026 ||
		int(req.Filters.CreatedAfter.Month()) != 1 || req.Filters.CreatedAfter.Day() != 1 {
		t.Errorf("CreatedAfter=%v", req.Filters.CreatedAfter)
	}
	if req.Filters.CreatedBefore == nil || int(req.Filters.CreatedBefore.Month()) != 4 ||
		req.Filters.CreatedBefore.Day() != 30 {
		t.Errorf("CreatedBefore=%v", req.Filters.CreatedBefore)
	}
}

func TestParseURL_OpenEndedSizeRange(t *testing.T) {
	r := httptest.NewRequest("GET",
		"/api/v1/search?filter=size:[100000,]", nil)
	r.Header.Set("X-Auth-Tenant-ID", "t1")
	r.Header.Set("X-User-ID", "u1")
	req := parseSearchRequestFromURL(r)

	if req.Filters.SizeMinBytes == nil || *req.Filters.SizeMinBytes != 100_000 {
		t.Errorf("SizeMinBytes=%v want 100000", req.Filters.SizeMinBytes)
	}
	if req.Filters.SizeMaxBytes != nil {
		t.Errorf("SizeMaxBytes=%v should be nil for open-ended right side", req.Filters.SizeMaxBytes)
	}
}

func TestParseURL_UnknownFilterFallsToCustomMetadata(t *testing.T) {
	// The whole point of CustomMetadata routing — a tenant should be
	// able to filter on any indexed JSONB field without per-field
	// code changes.
	r := httptest.NewRequest("GET",
		"/api/v1/search?filter=contract_value:high", nil)
	r.Header.Set("X-Auth-Tenant-ID", "t1")
	r.Header.Set("X-User-ID", "u1")
	req := parseSearchRequestFromURL(r)

	if got := req.Filters.CustomMetadata["contract_value"]; got != "high" {
		t.Errorf("CustomMetadata[contract_value]=%q want 'high'", got)
	}
}

func TestParseURL_RegionPinRoutesToTopLevelField(t *testing.T) {
	// region_pin is a top-level keyword on the index — must NOT land
	// in CustomMetadata (which would prefix `custom_metadata.` and
	// miss the field entirely).
	r := httptest.NewRequest("GET",
		"/api/v1/search?filter=region_pin:us-east", nil)
	r.Header.Set("X-Auth-Tenant-ID", "t1")
	r.Header.Set("X-User-ID", "u1")
	req := parseSearchRequestFromURL(r)

	if len(req.Filters.RegionPin) != 1 || req.Filters.RegionPin[0] != "us-east" {
		t.Errorf("RegionPin=%v want [us-east]", req.Filters.RegionPin)
	}
	if _, leaked := req.Filters.CustomMetadata["region_pin"]; leaked {
		t.Error("region_pin must not leak into CustomMetadata")
	}
}

func TestParseURL_MalformedFilterDropped(t *testing.T) {
	// `filter=just_a_key` (no colon) and `filter=` (empty) MUST be
	// dropped silently rather than 4xx — a stale client URL
	// shouldn't break search.
	r := httptest.NewRequest("GET",
		"/api/v1/search?filter=no_colon_here&filter=tag:contract", nil)
	r.Header.Set("X-Auth-Tenant-ID", "t1")
	r.Header.Set("X-User-ID", "u1")
	req := parseSearchRequestFromURL(r)

	if len(req.Filters.Tags) != 1 || req.Filters.Tags[0] != "contract" {
		t.Errorf("tags=%v — malformed filter must not block valid ones", req.Filters.Tags)
	}
}

func TestParseURL_FacetFromRepeatingAndCommaSeparated(t *testing.T) {
	// Both shapes accepted: ?facet=a,b&facet=c
	r := httptest.NewRequest("GET",
		"/api/v1/search?facet=tag,author&facet=region_pin", nil)
	r.Header.Set("X-Auth-Tenant-ID", "t1")
	r.Header.Set("X-User-ID", "u1")
	req := parseSearchRequestFromURL(r)

	want := []string{"tag", "author", "region_pin"}
	if len(req.Facets) != len(want) {
		t.Fatalf("facets=%v want %v", req.Facets, want)
	}
	for i, w := range want {
		if req.Facets[i] != w {
			t.Errorf("facets[%d]=%s want %s", i, req.Facets[i], w)
		}
	}
}

// TestParseURL_GroupHeaderIgnored pins Epic 9 #6: parseSearchRequestFromURL must
// NOT read ACL groups from the client-controllable X-Group-IDs header. Groups are
// set authoritatively by searchGET (callerGroups → session groups / DB), so the
// parsed request carries none regardless of what the client sends.
func TestParseURL_GroupHeaderIgnored(t *testing.T) {
	r := httptest.NewRequest("GET",
		"/api/v1/search?q=test", nil)
	r.Header.Set("X-Auth-Tenant-ID", "t1")
	r.Header.Set("X-User-ID", "u1")
	r.Header.Set("X-Group-IDs", "engineering, legal ,product")
	req := parseSearchRequestFromURL(r)

	if len(req.GroupIDs) != 0 {
		t.Errorf("groups=%v; want none (X-Group-IDs must be ignored, groups set authoritatively by searchGET)", req.GroupIDs)
	}
}
