package testutil

import "testing"

// The prod-posture lane exists to verify RLS fixes under the enforced
// NOBYPASSRLS posture (docs/STATE_OF_THE_PROJECT.md 2026-07-03). Its
// tests are meaningless if the dev bypass opt-in leaks into the lane's
// environment, so AssertProdPosture must hard-fail the moment
// SEDOC_ALLOW_BYPASS_RLS=1 is present.

func TestProdPostureViolation_BypassEnvSet(t *testing.T) {
	t.Setenv("SEDOC_ALLOW_BYPASS_RLS", "1")
	if v := ProdPostureViolation(); v == "" {
		t.Fatal("SEDOC_ALLOW_BYPASS_RLS=1 must be reported as a prod-posture violation")
	}
}

func TestProdPostureViolation_CleanEnv(t *testing.T) {
	t.Setenv("SEDOC_ALLOW_BYPASS_RLS", "")
	if v := ProdPostureViolation(); v != "" {
		t.Fatalf("unset bypass env reported as violation: %q", v)
	}
}

func TestProdPostureViolation_ExplicitZeroIsClean(t *testing.T) {
	// The runtime gate (pkg/database/rls_posture.go) only honors the
	// exact string "1"; "0" is the documented way to disarm the opt-in
	// in compose overrides, so it must not trip the assert either.
	t.Setenv("SEDOC_ALLOW_BYPASS_RLS", "0")
	if v := ProdPostureViolation(); v != "" {
		t.Fatalf("SEDOC_ALLOW_BYPASS_RLS=0 reported as violation: %q", v)
	}
}
