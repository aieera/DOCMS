package service

import (
	"slices"
	"testing"

	"github.com/aieera/sedoc/services/policy/internal/model"
)

// TestMayGrant pins the re-sharing rule: you need at least `share` on a
// resource to hand out any access at all, and you can never hand out more
// than you hold yourself.
//
// Before this rule existed the grant endpoint demanded `admin`, which made
// the documented hierarchy ("a role holding edit also holds share and view",
// handler/matrix.go) a lie — a Commenter or Editor could never share.
func TestMayGrant(t *testing.T) {
	cases := []struct {
		name      string
		caller    model.Capability
		requested model.Capability
		want      bool
	}{
		// No access, or view-only: sharing is not one of your rights.
		{"no capability cannot grant view", "", model.CapView, false},
		{"view holder cannot grant view", model.CapView, model.CapView, false},
		{"view holder cannot grant share", model.CapView, model.CapShare, false},

		// share is the entry point for re-sharing.
		{"share holder grants view", model.CapShare, model.CapView, true},
		{"share holder grants share", model.CapShare, model.CapShare, true},
		{"share holder cannot grant edit", model.CapShare, model.CapEdit, false},
		{"share holder cannot grant delete", model.CapShare, model.CapDelete, false},
		{"share holder cannot grant admin", model.CapShare, model.CapAdmin, false},

		// Higher capabilities imply share and everything below them.
		{"edit holder grants share", model.CapEdit, model.CapShare, true},
		{"edit holder grants edit", model.CapEdit, model.CapEdit, true},
		{"edit holder cannot grant delete", model.CapEdit, model.CapDelete, false},
		{"edit holder cannot grant admin", model.CapEdit, model.CapAdmin, false},

		{"delete holder grants edit", model.CapDelete, model.CapEdit, true},
		{"delete holder cannot grant admin", model.CapDelete, model.CapAdmin, false},

		{"admin grants admin", model.CapAdmin, model.CapAdmin, true},
		{"admin grants view", model.CapAdmin, model.CapView, true},

		// Unknown capabilities rank 0 and are refused in both positions.
		{"unknown caller capability", "nonsense", model.CapView, false},
		{"unknown requested capability", model.CapAdmin, "nonsense", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MayGrant(tc.caller, tc.requested); got != tc.want {
				t.Fatalf("MayGrant(%q, %q) = %v, want %v", tc.caller, tc.requested, got, tc.want)
			}
		})
	}
}

// TestGrantProbeOrder guards the order the handler probes capabilities in.
// It must run highest-first so the first allowed answer is the caller's
// actual ceiling, not merely the lowest right they happen to hold.
func TestGrantProbeOrder(t *testing.T) {
	probes := CapabilityProbeOrder()
	if len(probes) == 0 {
		t.Fatal("probe order must not be empty")
	}
	for i := 1; i < len(probes); i++ {
		if capRank(probes[i]) >= capRank(probes[i-1]) {
			t.Fatalf("probe order must descend: %s came after %s", probes[i], probes[i-1])
		}
	}
	if probes[0] != model.CapAdmin {
		t.Fatalf("highest probe must be admin, got %s", probes[0])
	}
	if probes[len(probes)-1] != model.CapShare {
		t.Fatalf("lowest probe must be share (below it nobody may grant), got %s", probes[len(probes)-1])
	}
}

// TestSupersededCapabilities pins which of a principal's existing grants a
// new grant replaces. Sharing someone at a new level must not leave the old
// level stacked behind it, but must never silently strip access the granter
// could not have handed out themselves.
func TestSupersededCapabilities(t *testing.T) {
	existing := []model.Capability{
		model.CapView, model.CapShare, model.CapEdit, model.CapDelete, model.CapAdmin,
	}

	t.Run("admin granting view supersedes every other level", func(t *testing.T) {
		got := supersededCapabilities(existing, model.CapView, model.CapAdmin)
		if len(got) != 4 {
			t.Fatalf("expected the other 4 levels superseded, got %v", got)
		}
		for _, c := range got {
			if c == model.CapView {
				t.Fatal("the capability being granted must not supersede itself")
			}
		}
	})

	t.Run("share holder cannot strip edit or admin", func(t *testing.T) {
		got := supersededCapabilities(existing, model.CapShare, model.CapShare)
		for _, c := range got {
			if capRank(c) > capRank(model.CapShare) {
				t.Fatalf("a share-level granter must not revoke %s", c)
			}
		}
		// It may still clear the redundant lower grant.
		if !slices.Contains(got, model.CapView) {
			t.Fatalf("expected view to be superseded by a share grant, got %v", got)
		}
	})

	t.Run("nothing to supersede when the principal holds only that level", func(t *testing.T) {
		got := supersededCapabilities([]model.Capability{model.CapView}, model.CapView, model.CapAdmin)
		if len(got) != 0 {
			t.Fatalf("expected no supersessions, got %v", got)
		}
	})

	t.Run("non-hierarchical capabilities are left alone", func(t *testing.T) {
		got := supersededCapabilities(
			[]model.Capability{"annotation.create", model.CapView},
			model.CapEdit, model.CapAdmin,
		)
		if slices.Contains(got, "annotation.create") {
			t.Fatal("annotation grants are orthogonal and must survive a level change")
		}
	})
}

// TestCapRankMatchesRego pins the Go mirror of the capability hierarchy to
// the literal in opa/policy.rego (`capability_includes`). The two are
// hand-encoded in different languages and have already drifted once —
// view_unredacted was missing from the Go side, silently reclassifying it
// as "orthogonal" in supersession decisions.
func TestCapRankMatchesRego(t *testing.T) {
	rego := map[model.Capability]int{
		model.CapAdmin:    50,
		model.CapDelete:   40,
		model.CapEdit:     30,
		model.CapShare:    20,
		"view_unredacted": 15,
		model.CapView:     10,
	}
	for c, want := range rego {
		if got := capRank(c); got != want {
			t.Errorf("capRank(%q) = %d, want %d (opa/policy.rego hierarchy)", c, got, want)
		}
	}
	if got := capRank("not-a-capability"); got != 0 {
		t.Errorf("unknown capability must rank 0, got %d", got)
	}
}

// A grant with an empty granter capability must never supersede anything —
// the service treats it as rank 0 (fail-closed), not as implicit admin.
func TestSupersededCapabilitiesEmptyGranter(t *testing.T) {
	got := supersededCapabilities(
		[]model.Capability{model.CapView, model.CapEdit}, model.CapShare, "",
	)
	if len(got) != 0 {
		t.Fatalf("empty granter capability must supersede nothing, got %v", got)
	}
}
