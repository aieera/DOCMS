package model

import "testing"

func TestRequiredClearance_IdentityLadder(t *testing.T) {
	// No rules → identity ladder: a confidential doc needs confidential clearance.
	if got := RequiredClearance(nil, ClassConfidential, false, "view"); got != ClassConfidential {
		t.Fatalf("identity default: want confidential, got %q", got)
	}
	// Unclassified needs nothing.
	if got := RequiredClearance(nil, ClassUnclassified, false, "view"); got != "" {
		t.Fatalf("unclassified: want empty, got %q", got)
	}
}

func TestEffectiveClassification_PHIBackstop(t *testing.T) {
	cfg := ClassificationGateConfig{Enabled: true, PHIRequiresRestricted: true}
	// A PHI doc with no explicit level is treated as restricted.
	if got := EffectiveClassification(cfg, "", true, false); got != ClassRestricted {
		t.Fatalf("PHI backstop: want restricted, got %q", got)
	}
	// PII bumps to at least internal.
	if got := EffectiveClassification(cfg, "", false, true); got != ClassInternal {
		t.Fatalf("PII: want internal, got %q", got)
	}
	// Backstop off → PHI does not bump.
	off := ClassificationGateConfig{Enabled: true, PHIRequiresRestricted: false}
	if got := EffectiveClassification(off, ClassInternal, true, false); got != ClassInternal {
		t.Fatalf("backstop off: want internal, got %q", got)
	}
}

func TestRequiredClearance_StrictestRuleWins(t *testing.T) {
	rules := []ClassificationRule{
		{MinClassification: ClassInternal, Action: "view", RequiredClearance: ClassInternal},
		{MinClassification: ClassInternal, Action: "*", RequiredClearance: ClassConfidential},
	}
	// Both rules match a confidential doc on view; the stricter (confidential) wins.
	if got := RequiredClearance(rules, ClassConfidential, false, "view"); got != ClassConfidential {
		t.Fatalf("strictest rule: want confidential, got %q", got)
	}
}

func TestRequiredClearance_DownloadAliasesView(t *testing.T) {
	rules := []ClassificationRule{
		{MinClassification: ClassUnclassified, Action: "download", RequiredClearance: ClassRestricted},
	}
	// A download rule constrains the view check used by the download path.
	if got := RequiredClearance(rules, ClassInternal, false, "view"); got != ClassRestricted {
		t.Fatalf("download alias: want restricted, got %q", got)
	}
}

func TestRequiredClearance_PHIRule(t *testing.T) {
	rules := []ClassificationRule{
		{MinClassification: ClassRestricted, Action: "*", RequiredClearance: ClassRestricted, AppliesToPHI: true},
	}
	// applies_to_phi makes the rule fire on a PHI doc even if its level is low.
	if got := RequiredClearance(rules, ClassInternal, true, "view"); got != ClassRestricted {
		t.Fatalf("phi rule: want restricted, got %q", got)
	}
}
