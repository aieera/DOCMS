package service

import (
	"slices"
	"testing"

	"github.com/google/uuid"

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
// perms builds active grants of the given levels, all made by `by`.
func perms(by uuid.UUID, caps ...model.Capability) []model.Permission {
	out := make([]model.Permission, 0, len(caps))
	for _, c := range caps {
		out = append(out, model.Permission{ID: uuid.New(), Capability: c, GrantedBy: by})
	}
	return out
}

func capsOf(ps []model.Permission) []model.Capability {
	out := make([]model.Capability, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Capability)
	}
	return out
}

func TestSupersededCapabilities(t *testing.T) {
	me, peer := uuid.New(), uuid.New()
	all := []model.Capability{model.CapView, model.CapShare, model.CapEdit, model.CapDelete, model.CapAdmin}

	t.Run("org admin granting view supersedes every other level", func(t *testing.T) {
		got := capsOf(supersededCapabilities(perms(peer, all...), model.CapView,
			supersedeGranter{ID: me, Cap: model.CapAdmin, OrgAdmin: true}))
		if len(got) != 4 || slices.Contains(got, model.CapView) {
			t.Fatalf("expected the other 4 levels superseded, got %v", got)
		}
	})

	t.Run("share holder cannot strip edit or admin", func(t *testing.T) {
		got := capsOf(supersededCapabilities(perms(me, all...), model.CapShare,
			supersedeGranter{ID: me, Cap: model.CapShare}))
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
		got := supersededCapabilities(perms(peer, model.CapView), model.CapView,
			supersedeGranter{ID: me, Cap: model.CapAdmin, OrgAdmin: true})
		if len(got) != 0 {
			t.Fatalf("expected no supersessions, got %v", capsOf(got))
		}
	})

	t.Run("non-hierarchical capabilities are left alone", func(t *testing.T) {
		got := capsOf(supersededCapabilities(perms(peer, "annotation.create", model.CapView),
			model.CapEdit, supersedeGranter{ID: me, Cap: model.CapAdmin, OrgAdmin: true}))
		if slices.Contains(got, "annotation.create") {
			t.Fatal("annotation grants are orthogonal and must survive a level change")
		}
	})
}

// L-8: the supersede rule only refused levels ABOVE the granter's own, so a
// share-level granter could demote a peer's equal share grant (and a
// resource admin a peer admin's grant) by re-sharing at a lower level. A
// level equal to the granter's ceiling may now be superseded only when the
// granter made that grant; strictly lower levels and org admins are
// unchanged.
func TestGrant_CannotDemotePeer(t *testing.T) {
	me, peer := uuid.New(), uuid.New()

	got := supersededCapabilities(perms(peer, model.CapShare), model.CapView,
		supersedeGranter{ID: me, Cap: model.CapShare})
	if len(got) != 0 {
		t.Fatalf("a share granter must not demote a peer's share grant, superseded %v", capsOf(got))
	}
	got = supersededCapabilities(perms(peer, model.CapAdmin), model.CapEdit,
		supersedeGranter{ID: me, Cap: model.CapAdmin})
	if len(got) != 0 {
		t.Fatalf("a resource admin must not demote a peer's admin grant, superseded %v", capsOf(got))
	}

	// The granter's own equal-level grant may be changed.
	got = supersededCapabilities(perms(me, model.CapShare), model.CapView,
		supersedeGranter{ID: me, Cap: model.CapShare})
	if len(got) != 1 {
		t.Fatalf("a granter may change a share grant they made, superseded %v", capsOf(got))
	}
	// Levels strictly below the ceiling are superseded whoever made them.
	got = supersededCapabilities(perms(peer, model.CapView), model.CapShare,
		supersedeGranter{ID: me, Cap: model.CapEdit})
	if len(got) != 1 {
		t.Fatalf("a lower level must be superseded, got %v", capsOf(got))
	}
	// A tenant owner/admin outranks every grant.
	got = supersededCapabilities(perms(peer, model.CapAdmin), model.CapEdit,
		supersedeGranter{ID: me, Cap: model.CapAdmin, OrgAdmin: true})
	if len(got) != 1 {
		t.Fatalf("an org admin may change any level, got %v", capsOf(got))
	}
}

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
	me := uuid.New()
	got := supersededCapabilities(perms(me, model.CapView, model.CapEdit), model.CapShare,
		supersedeGranter{ID: me})
	if len(got) != 0 {
		t.Fatalf("empty granter capability must supersede nothing, got %v", capsOf(got))
	}
}
