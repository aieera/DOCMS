package bucket

import (
	"fmt"
	"testing"
)

func TestLabelDeterministic(t *testing.T) {
	for _, ref := range []string{"CUST-1", "acme", "INV-9", ""} {
		if Label(ref, 256) != Label(ref, 256) {
			t.Fatalf("%q not deterministic", ref)
		}
	}
}

func TestLabelDistribution(t *testing.T) {
	counts := map[string]int{}
	const n = 10000
	for i := 0; i < n; i++ {
		counts[Label(fmt.Sprintf("CUST-%06d", i), 256)]++
	}
	if len(counts) < 100 {
		t.Fatalf("expected wide spread across buckets, got %d", len(counts))
	}
	max := 0
	for _, c := range counts {
		if c > max {
			max = c
		}
	}
	if max > 200 { // ~39 avg over 256 buckets; uniform hash keeps the worst bounded
		t.Fatalf("hottest bucket %d too large", max)
	}
}

func TestLabelDefaultsAndFormat(t *testing.T) {
	if got := Label("CUST-1", 0); len(got) != 4 || got[0] != 'b' {
		t.Fatalf("label format = %q, want b<3 digits>", got)
	}
}
