package service

// Pure-Go unit tests for Wave 15.3 password-change plumbing. Integration
// tests that exercise Redis + Postgres are scheduled behind the
// //go:build integration tag (see tests/integration/auth) — they need the
// testcontainers harness to spin real infra, which this package's pure
// unit-test suite deliberately avoids.

import (
	"testing"

	"github.com/stretchr/testify/require"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
)

// The change-password path reuses validatePassword. Ensure its policy
// rejects the obvious weak shapes a force-reset user might type in:
// a 12-char password that's still all lowercase, a super-common
// sequence, etc. The integration test verifies the last-5-reuse
// behaviour end-to-end.
func TestChangePassword_RejectsWeakCandidates(t *testing.T) {
	bad := []string{
		"password1234",     // no upper, no special
		"PASSWORD1234!",    // no lower
		"Password!!!!",     // no digit
		"Short1!A",         // length < 12
	}
	for _, pw := range bad {
		err := validatePassword(pw)
		require.Error(t, err, pw)
		require.Equal(t, vdmserr.KindValidation, vdmserr.KindOf(err), pw)
	}
}

// TestChangePasswordReasonsAreStable pins the enum strings that ship
// to subscribers on dms.auth.password_changed.v1 so consumers don't
// regress when the internal type is refactored.
func TestChangePasswordReasonsAreStable(t *testing.T) {
	require.Equal(t, ChangePasswordReason("admin_reset"), ReasonAdminReset)
	require.Equal(t, ChangePasswordReason("expiry"), ReasonExpiry)
	require.Equal(t, ChangePasswordReason("first_login"), ReasonFirstLogin)
	require.Equal(t, ChangePasswordReason("self"), ReasonSelfInitiated)
	require.Equal(t, ChangePasswordReason("hibp_compromise"), ReasonHIBPCompromise)
}

// TestPasswordChangeTokenTTLReasonable guards against an accidental
// change to the single-use-token lifetime: too short locks users out,
// too long widens the window a stolen token is useful.
func TestPasswordChangeTokenTTLReasonable(t *testing.T) {
	require.GreaterOrEqual(t, int(PasswordChangeTokenTTL.Minutes()), 5,
		"too short — real users will fail to complete in time")
	require.LessOrEqual(t, int(PasswordChangeTokenTTL.Minutes()), 15,
		"too long — widens the stolen-token window")
}

// TestPasswordHistoryKeepAtLeast5 guards the "reject reuse against last
// 5" requirement in the DoD. A regression to 1 would silently weaken
// the policy.
func TestPasswordHistoryKeepAtLeast5(t *testing.T) {
	require.GreaterOrEqual(t, PasswordHistoryKeep, 5)
}
