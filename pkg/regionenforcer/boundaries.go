// Package regionenforcer captures the geopolitical boundary model that
// governs cross-region data movement. The DB's supported_regions table
// is the source of truth for region codes and their boundary
// assignments; this package mirrors it in Go so hot-path code doesn't
// need a DB round-trip to decide whether a move is permitted.
//
// If supported_regions changes (new region, boundary reclassification),
// the mirror below must be updated in the same migration — a CI test
// in this package enforces the parity.
package regionenforcer

import (
	"fmt"
	"strings"
)

// Boundary is one of EU / US / MENA / APAC / OTHER. Cross-boundary
// document movement is rejected as a REGION_VIOLATION unless an
// explicit exception workflow signs off (not wired today — all
// cross-boundary attempts fail closed).
type Boundary string

const (
	BoundaryEU    Boundary = "EU"
	BoundaryUS    Boundary = "US"
	BoundaryMENA  Boundary = "MENA"
	BoundaryAPAC  Boundary = "APAC"
	BoundaryOther Boundary = "OTHER"
)

// regionBoundary mirrors supported_regions in migration 000014. Keeping
// this map exhaustive is an invariant — TestBoundariesMatchMigration
// diffs it against the .sql seed.
var regionBoundary = map[string]Boundary{
	"us-east-1":      BoundaryUS,
	"us-west-2":      BoundaryUS,
	"eu-west-1":      BoundaryEU,
	"eu-central-1":   BoundaryEU,
	"me-south-1":     BoundaryMENA,
	"ap-southeast-1": BoundaryAPAC,
	"ap-northeast-1": BoundaryAPAC,
	"custom":         BoundaryOther,
}

// BoundaryOf returns the geopolitical boundary for a region code, or
// BoundaryOther if the code is unknown. Unknown codes fail closed in
// SameBoundary — comparing OTHER with OTHER doesn't count as a
// boundary match.
func BoundaryOf(region string) Boundary {
	if b, ok := regionBoundary[strings.ToLower(strings.TrimSpace(region))]; ok {
		return b
	}
	return BoundaryOther
}

// SameBoundary reports whether two regions sit inside the same
// geopolitical boundary. OTHER never matches OTHER — custom/on-prem
// regions are explicitly "bring your own contract" and we refuse to
// infer a policy decision from the platform side.
func SameBoundary(a, b string) bool {
	ba := BoundaryOf(a)
	bb := BoundaryOf(b)
	if ba == BoundaryOther || bb == BoundaryOther {
		return false
	}
	return ba == bb
}

// IsKnown reports whether a region code appears in the mirror. Service
// layers use this to reject obviously typo'd regions before hitting
// the DB.
func IsKnown(region string) bool {
	_, ok := regionBoundary[strings.ToLower(strings.TrimSpace(region))]
	return ok
}

// ErrRegionViolation is the canonical error returned by Validate. The
// Reason tag matches the audit-event `reason` field so operators can
// pivot between the two surfaces cleanly.
type ErrRegionViolation struct {
	DocumentRegion string
	TargetRegion   string
	Layer          string // storage | search | cache | log | backup | cdn
	Reason         string // unknown_region | cross_boundary | not_allowed | layer_mismatch
}

func (e *ErrRegionViolation) Error() string {
	return fmt.Sprintf("region violation: doc=%s target=%s layer=%s reason=%s",
		e.DocumentRegion, e.TargetRegion, e.Layer, e.Reason)
}

// Validate checks that target is safe to use for a document pinned to
// docRegion within a given layer. Used by middleware + service code
// before any cross-region operation lands.
func Validate(docRegion, target, layer string) error {
	if !IsKnown(docRegion) || !IsKnown(target) {
		return &ErrRegionViolation{DocumentRegion: docRegion, TargetRegion: target, Layer: layer, Reason: "unknown_region"}
	}
	if strings.EqualFold(docRegion, target) {
		return nil
	}
	if !SameBoundary(docRegion, target) {
		return &ErrRegionViolation{DocumentRegion: docRegion, TargetRegion: target, Layer: layer, Reason: "cross_boundary"}
	}
	// Same boundary, different region — allowed only via residency
	// migration workflow, which uses its own code path and doesn't
	// come through this validator.
	return &ErrRegionViolation{DocumentRegion: docRegion, TargetRegion: target, Layer: layer, Reason: "layer_mismatch"}
}
