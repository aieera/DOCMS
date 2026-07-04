package service

import (
	"testing"

	"github.com/aieera/sedoc/services/policy/internal/model"
)

// TestCapRank pins the capability hierarchy used to pick a principal's
// effective (highest) capability. Must stay in lockstep with the Rego
// policy's ordering (admin > delete > edit > share > view).
func TestCapRank(t *testing.T) {
	order := []model.Capability{
		model.CapView, model.CapShare, model.CapEdit, model.CapDelete, model.CapAdmin,
	}
	for i := 1; i < len(order); i++ {
		if capRank(order[i]) <= capRank(order[i-1]) {
			t.Fatalf("expected %s to outrank %s", order[i], order[i-1])
		}
	}
	if capRank("nonsense") != 0 {
		t.Fatalf("unknown capability should rank 0")
	}
	// The merge keeps the max: admin must beat view.
	if capRank(model.CapAdmin) <= capRank(model.CapView) {
		t.Fatal("admin must outrank view")
	}
}
