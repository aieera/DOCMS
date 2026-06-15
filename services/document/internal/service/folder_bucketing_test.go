package service

import (
	"fmt"
	"testing"
)

// TestComputeBuckets_Deterministic: the same (ref, region) always yields the
// same bucket labels — the property the integration relies on to find a
// customer's folder again without storing the mapping.
func TestComputeBuckets_Deterministic(t *testing.T) {
	scheme := FolderBucketScheme{Mode: BucketModeHash, PrefixLen: 2}
	for _, ref := range []string{"CUST-1", "acme corp", "INV-2024-00188", ""} {
		a := computeBuckets(scheme, ref, "")
		b := computeBuckets(scheme, ref, "")
		if len(a) != 1 || a[0] != b[0] {
			t.Fatalf("ref %q not deterministic: %v vs %v", ref, a, b)
		}
		if len(a[0]) != 2 {
			t.Fatalf("ref %q bucket %q not 2 chars", ref, a[0])
		}
	}
}

// TestComputeBuckets_HashDistribution: 10k customers spread across the 256
// two-hex-char buckets so no single bucket holds anywhere near "tens of
// thousands". This is the core scaling guarantee.
func TestComputeBuckets_HashDistribution(t *testing.T) {
	scheme := FolderBucketScheme{Mode: BucketModeHash, PrefixLen: 2}
	counts := map[string]int{}
	const n = 10000
	for i := 0; i < n; i++ {
		b := computeBuckets(scheme, fmt.Sprintf("CUST-%06d", i), "")
		counts[b[0]]++
	}
	max := 0
	for _, c := range counts {
		if c > max {
			max = c
		}
	}
	if len(counts) < 100 {
		t.Fatalf("expected wide spread, got only %d distinct buckets", len(counts))
	}
	// avg is ~39; a uniform hash keeps the worst bucket well bounded.
	if max > 200 {
		t.Fatalf("hottest bucket has %d (expected << tens of thousands)", max)
	}
}

func TestComputeBuckets_Modes(t *testing.T) {
	// id_prefix: leading slugged chars.
	if got := computeBuckets(FolderBucketScheme{Mode: BucketModeIDPrefix, PrefixLen: 3}, "ACME-Corp", ""); got[0] != "acm" {
		t.Fatalf("id_prefix bucket = %q, want acm", got[0])
	}
	// region: [region, hashSub].
	got := computeBuckets(FolderBucketScheme{Mode: BucketModeRegion, PrefixLen: 2}, "CUST-1", "EU-West-1")
	if len(got) != 2 || got[0] != "eu_west_1" || len(got[1]) != 2 {
		t.Fatalf("region buckets = %v, want [eu_west_1 <2 hex>]", got)
	}
	// region with empty region → "global".
	if g := computeBuckets(FolderBucketScheme{Mode: BucketModeRegion}, "CUST-1", ""); g[0] != "global" {
		t.Fatalf("empty region top = %q, want global", g[0])
	}
	// unknown mode falls back to hash.
	if g := computeBuckets(FolderBucketScheme{Mode: "nonsense"}, "CUST-1", ""); len(g) != 1 || len(g[0]) != 2 {
		t.Fatalf("unknown mode should fall back to hash/2, got %v", g)
	}
}

// TestPlaceCustomerFolder_Defaults: a zero-value service uses hash/2.
func TestPlaceCustomerFolder_Defaults(t *testing.T) {
	s := &DocumentService{}
	if got := s.FolderBucketScheme(); got.Mode != BucketModeHash || got.PrefixLen != 2 {
		t.Fatalf("default scheme = %+v, want hash/2", got)
	}
	b := s.PlaceCustomerFolder("CUST-42", "")
	if len(b) != 1 || len(b[0]) != 2 {
		t.Fatalf("PlaceCustomerFolder default = %v, want one 2-char bucket", b)
	}
}

func TestPlaceCustomerFolder_Configured(t *testing.T) {
	s := &DocumentService{}
	s.SetFolderBucketScheme(FolderBucketScheme{Mode: BucketModeRegion, PrefixLen: 3})
	b := s.PlaceCustomerFolder("CUST-42", "us-east-1")
	if len(b) != 2 || b[0] != "us_east_1" || len(b[1]) != 3 {
		t.Fatalf("configured PlaceCustomerFolder = %v, want [us_east_1 <3 hex>]", b)
	}
}
