// ADR 0065 — facet cache key invariants.
//
// We don't unit-test the Redis read/write path here (covered by
// integration tests against a real container); instead we pin the
// key-generation rules that prevent cache poisoning across tenants
// and across users-with-different-groups.
package service

import (
	"testing"
	"time"

	"github.com/vaultdms/vaultdms/services/search/internal/model"
)

func makeReq(tenant, user string, groups []string, q string, tags []string) *model.SearchRequest {
	return &model.SearchRequest{
		TenantID: tenant, UserID: user, GroupIDs: groups,
		Query: q,
		Filters: model.SearchFilters{
			Tags: tags,
		},
		Facets: []string{"tag", "author"},
	}
}

func TestFacetCacheKey_DifferentTenantsNeverShare(t *testing.T) {
	a := makeReq("tenant-A", "u", nil, "contract", nil)
	b := makeReq("tenant-B", "u", nil, "contract", nil)
	if facetCacheKey(a) == facetCacheKey(b) {
		t.Fatal("cache key must differ across tenants — would leak buckets cross-tenant")
	}
}

func TestFacetCacheKey_DifferentGroupsNeverShare(t *testing.T) {
	// Same tenant + same user identity, different group memberships:
	// must NOT share a cache entry, because readable_by-scoped
	// bucket counts depend on the group set.
	a := makeReq("t", "alice", []string{"engineering"}, "q", nil)
	b := makeReq("t", "alice", []string{"engineering", "legal"}, "q", nil)
	if facetCacheKey(a) == facetCacheKey(b) {
		t.Fatal("cache key must differ when groups differ — counts depend on readable_by")
	}
}

func TestFacetCacheKey_DifferentTagFiltersNeverShare(t *testing.T) {
	a := makeReq("t", "u", nil, "q", []string{"contract"})
	b := makeReq("t", "u", nil, "q", []string{"invoice"})
	if facetCacheKey(a) == facetCacheKey(b) {
		t.Fatal("cache key must differ when tag filter differs")
	}
}

func TestFacetCacheKey_StableAcrossSliceReorders(t *testing.T) {
	// The same logical request encoded with differently-ordered
	// slices (groups came back from a map iter, tags from URL
	// repeated params) must hash to the same key — otherwise we'd
	// miss the cache on every other request.
	a := makeReq("t", "u", []string{"engineering", "legal"}, "q", []string{"a", "b"})
	b := makeReq("t", "u", []string{"legal", "engineering"}, "q", []string{"b", "a"})
	if facetCacheKey(a) != facetCacheKey(b) {
		t.Fatal("cache key must be stable across slice re-orderings")
	}
}

func TestFacetCacheKey_StableAcrossFacetReorders(t *testing.T) {
	a := &model.SearchRequest{
		TenantID: "t", UserID: "u", Facets: []string{"tag", "author"},
	}
	b := &model.SearchRequest{
		TenantID: "t", UserID: "u", Facets: []string{"author", "tag"},
	}
	if facetCacheKey(a) != facetCacheKey(b) {
		t.Fatal("cache key must be stable across facet ordering")
	}
}

func TestFacetCacheKey_DifferentDateRangesNeverShare(t *testing.T) {
	jan := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	feb := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	a := &model.SearchRequest{
		TenantID: "t", UserID: "u",
		Filters: model.SearchFilters{CreatedAfter: &jan},
	}
	b := &model.SearchRequest{
		TenantID: "t", UserID: "u",
		Filters: model.SearchFilters{CreatedAfter: &feb},
	}
	if facetCacheKey(a) == facetCacheKey(b) {
		t.Fatal("cache key must differ when CreatedAfter differs")
	}
}

func TestFacetCacheKey_RegionAndAuthorIncluded(t *testing.T) {
	// New filter slices added in commit d05b244 — must be in the
	// cache key shape, otherwise selecting a region in the URL
	// would silently serve buckets from a different region scope.
	a := &model.SearchRequest{
		TenantID: "t", UserID: "u",
		Filters: model.SearchFilters{RegionPin: []string{"us-east"}},
	}
	b := &model.SearchRequest{
		TenantID: "t", UserID: "u",
		Filters: model.SearchFilters{RegionPin: []string{"eu-west"}},
	}
	if facetCacheKey(a) == facetCacheKey(b) {
		t.Fatal("cache key must include RegionPin")
	}

	c := &model.SearchRequest{
		TenantID: "t", UserID: "u",
		Filters: model.SearchFilters{CreatedByName: []string{"alice"}},
	}
	d := &model.SearchRequest{
		TenantID: "t", UserID: "u",
		Filters: model.SearchFilters{CreatedByName: []string{"bob"}},
	}
	if facetCacheKey(c) == facetCacheKey(d) {
		t.Fatal("cache key must include CreatedByName (author)")
	}
}
