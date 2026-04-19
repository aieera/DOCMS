package model

import "testing"

// FeatureFlags are the hinge between plan tier and product capability.
// Pin the plan→flag mapping so a regression changes the table obviously.

func TestDefaultFlagsByPlan_Standard(t *testing.T) {
	f := DefaultFlagsByPlan("standard")
	if f.AIEnabled || f.AdvancedWorkflow || f.SSOEnabled || f.ESignatures || f.CustomBranding || f.DataRooms {
		t.Errorf("standard should have all paid flags off, got %+v", f)
	}
	if !f.APIAccess {
		t.Error("standard should keep APIAccess on (documented baseline)")
	}
}

func TestDefaultFlagsByPlan_Enterprise(t *testing.T) {
	f := DefaultFlagsByPlan("enterprise")
	// Every paid feature EXCEPT data rooms.
	if !f.AIEnabled || !f.AdvancedWorkflow || !f.SSOEnabled ||
		!f.ESignatures || !f.CustomBranding || !f.APIAccess {
		t.Errorf("enterprise missing an expected flag: %+v", f)
	}
	if f.DataRooms {
		t.Error("enterprise should NOT have data rooms (that's dedicated-only)")
	}
}

func TestDefaultFlagsByPlan_Dedicated(t *testing.T) {
	f := DefaultFlagsByPlan("dedicated")
	if !f.DataRooms {
		t.Error("dedicated must have data rooms enabled")
	}
	if !(f.AIEnabled && f.AdvancedWorkflow && f.SSOEnabled && f.ESignatures && f.CustomBranding && f.APIAccess) {
		t.Errorf("dedicated missing an expected flag: %+v", f)
	}
}

func TestDefaultFlagsByPlan_UnknownFallsBackToStandard(t *testing.T) {
	f := DefaultFlagsByPlan("nonexistent-plan")
	std := DefaultFlagsByPlan("standard")
	if f != std {
		t.Errorf("unknown plan should mirror standard, got %+v vs %+v", f, std)
	}
}

func TestDefaultPlans_StandardSubscribeLimits(t *testing.T) {
	std, ok := DefaultPlans["standard"]
	if !ok {
		t.Fatal("standard plan missing from catalog")
	}
	if std.StorageLimitGB != 100 {
		t.Errorf("standard storage: got %d, want 100", std.StorageLimitGB)
	}
	if std.MaxUsers != 25 {
		t.Errorf("standard max users: got %d, want 25", std.MaxUsers)
	}
	if std.PriceMonthly != 9900 {
		t.Errorf("standard price: got %d, want 9900 cents", std.PriceMonthly)
	}
}

func TestDefaultPlans_DedicatedUnlimited(t *testing.T) {
	// Dedicated uses -1 sentinel for "unlimited" — this contract is
	// read by the metering cron; changing it silently would break
	// usage enforcement.
	d := DefaultPlans["dedicated"]
	if d.MaxUsers != -1 {
		t.Errorf("dedicated MaxUsers should be -1 (unlimited), got %d", d.MaxUsers)
	}
	if d.IncludedOCRPages != -1 {
		t.Errorf("dedicated OCR pages should be -1, got %d", d.IncludedOCRPages)
	}
	if d.IncludedAITokens != -1 {
		t.Errorf("dedicated AI tokens should be -1, got %d", d.IncludedAITokens)
	}
}
