package service

import "github.com/aieera/sedoc/services/policy/internal/model"

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

// supersededCapabilities picks which of a principal's existing grants a new
// `granted` capability replaces, so changing somebody's access level does not
// leave the previous level stacked behind it.
//
// Two guards:
//   - capabilities outside the hierarchy (annotation.create, …) rank 0 and
//     are orthogonal to an access level, so they always survive;
//   - a grant never revokes a level above what the granter themselves holds,
//     which stops a share-level user from stripping an admin's access by
//     re-sharing the document at a lower level. The principal simply keeps
//     the higher grant.
func supersededCapabilities(existing []model.Capability, granted, granterCap model.Capability) []model.Capability {
	ceiling := capRank(granterCap)
	var out []model.Capability
	for _, c := range existing {
		if c == granted {
			continue
		}
		rank := capRank(c)
		if rank == 0 || rank > ceiling {
			continue
		}
		out = append(out, c)
	}
	return out
}
