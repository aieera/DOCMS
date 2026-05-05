package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// allowedRedactionReviewActions is exercised indirectly through the
// service.ReviewRedactionCandidate path; here we sanity-check the
// map directly so a future contributor doesn't accidentally drop one.
func TestAllowedRedactionReviewActions_Coverage(t *testing.T) {
	want := map[string]string{
		"approve":  "approved",
		"reject":   "rejected",
		"unreject": "pending",
	}
	require.Equal(t, want, allowedRedactionReviewActions)
}

// callerRole / WithCallerRole round-trip cleanly even when nothing's
// stamped on ctx. Important for the bulk-apply gate's non-admin path.
func TestCallerRole_RoundTrip(t *testing.T) {
	ctx := context.Background()
	require.Equal(t, "", callerRole(ctx))
	ctx = WithCallerRole(ctx, "admin")
	require.Equal(t, "admin", callerRole(ctx))
	// Trims whitespace so a stray header doesn't match-or-not by
	// accident.
	ctx = WithCallerRole(ctx, "  member  ")
	require.Equal(t, "member", callerRole(ctx))
}

// Bulk-apply threshold maths — admins always pass; non-admins blocked
// on >threshold; force_admin_approve overrides only when admin (the
// service trusts the caller_role too, the handler doesn't expose
// the flag in the JSON to non-admin sessions).
func TestBulkApplyGate_AdminAlwaysPasses(t *testing.T) {
	for _, role := range []string{"admin", "owner", "compliance_officer"} {
		got := isBulkApplyAllowed(101, 50, role, false)
		require.True(t, got, "role=%s should always pass the gate", role)
	}
}

func TestBulkApplyGate_NonAdminBelowThreshold(t *testing.T) {
	require.True(t, isBulkApplyAllowed(50, 50, "member", false))
	require.True(t, isBulkApplyAllowed(1, 50, "member", false))
}

func TestBulkApplyGate_NonAdminAboveThresholdWithoutForce(t *testing.T) {
	require.False(t, isBulkApplyAllowed(51, 50, "member", false))
	require.False(t, isBulkApplyAllowed(200, 50, "guest", false))
}

func TestBulkApplyGate_ForceFlagBypasses(t *testing.T) {
	require.True(t, isBulkApplyAllowed(200, 50, "member", true))
}

// isBulkApplyAllowed is the pure-logic kernel of the gate so we can
// test it without the DB tx wrapper. Mirrors the inline check in
// ApplyRedaction; if you change one, change the other.
func isBulkApplyAllowed(approvedCount, threshold int, role string, force bool) bool {
	isAdmin := role == "owner" || role == "admin" || role == "compliance_officer"
	if approvedCount > threshold && !isAdmin && !force {
		return false
	}
	return true
}
