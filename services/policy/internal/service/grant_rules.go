package service

import (
	"github.com/google/uuid"

	"github.com/aieera/sedoc/services/policy/internal/model"
)

// Re-sharing rules.
//
// The capability hierarchy (admin > delete > edit > share > view) is
// documented in handler/matrix.go and enforced in opa/policy.rego. `share`
// is the right to hand access to somebody else; everything above it implies
// it. Until these rules existed the grant endpoint required `admin`, so a
// principal granted "Commenter — can view and share" or "Editor — can view,
// edit, and share" could not actually share anything.

// MayGrant reports whether a caller holding callerCap on a resource may grant
// `requested` on it. Two conditions, both necessary:
//
//   - the caller must hold at least `share` — sharing is a capability, not a
//     side effect of being able to read;
//   - the caller must hold at least what they are handing out — nobody
//     bootstraps themselves (or a friend) upward through the share dialog.
//
// Callers with no capability, or an unrecognised one, rank 0 and are refused.
func MayGrant(callerCap, requested model.Capability) bool {
	callerRank := capRank(callerCap)
	requestedRank := capRank(requested)
	if requestedRank == 0 {
		return false
	}
	if callerRank < capRank(model.CapShare) {
		return false
	}
	return callerRank >= requestedRank
}

// CapabilityProbeOrder is the sequence a caller's own capability is resolved
// in — highest first, stopping at the first allowed answer so the result is
// their ceiling. It stops at `share` because below that nobody may grant, so
// probing `view` would buy nothing.
func CapabilityProbeOrder() []model.Capability {
	return []model.Capability{
		model.CapAdmin,
		model.CapDelete,
		model.CapEdit,
		model.CapShare,
	}
}

// supersedeGranter is who is granting, for the supersede rule.
type supersedeGranter struct {
	ID       uuid.UUID
	Cap      model.Capability // what the granter holds on the resource
	OrgAdmin bool             // tenant owner/admin: outranks every grant
}

// supersededCapabilities picks which of a principal's existing grants a new
// `granted` capability replaces, so changing somebody's access level does not
// leave the previous level stacked behind it.
//
// Guards:
//   - capabilities outside the hierarchy (annotation.create, …) rank 0 and
//     are orthogonal to an access level, so they always survive;
//   - a grant never revokes a level above what the granter holds, which
//     stops a share-level user from stripping an admin's access by
//     re-sharing at a lower level;
//   - a level EQUAL to the granter's own is revoked only when the granter
//     made that grant (L-8): otherwise a share holder could demote a peer's
//     share grant, or a resource admin a peer admin's, by re-sharing lower.
//     The principal then simply keeps the peer's grant.
//
// A tenant owner/admin outranks every resource grant and may supersede any
// level. An empty granter capability ranks 0 and supersedes nothing.
func supersededCapabilities(existing []model.Permission, granted model.Capability, g supersedeGranter) []model.Permission {
	ceiling := capRank(g.Cap)
	var out []model.Permission
	for _, p := range existing {
		if p.Capability == granted {
			continue
		}
		rank := capRank(p.Capability)
		switch {
		case rank == 0:
			continue
		case g.OrgAdmin && ceiling > 0:
			// outranks every resource grant
		case rank > ceiling:
			continue
		case rank == ceiling && p.GrantedBy != g.ID:
			continue
		}
		out = append(out, p)
	}
	return out
}
