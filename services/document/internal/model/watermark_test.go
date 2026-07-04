package model

import "testing"

func baseCfg() WatermarkConfig {
	return WatermarkConfig{Enabled: true, Template: "{email}", Opacity: 15, RotationDeg: 30, Tile: true, FontSize: 28, Color: "#808080"}
}

func TestResolveWatermark_NoOverrides(t *testing.T) {
	eff := ResolveWatermark(baseCfg(), nil, ClassConfidential, false)
	if !eff.Enabled || eff.Opacity != 15 || !eff.Tile {
		t.Fatalf("passthrough expected, got %+v", eff)
	}
}

func TestResolveWatermark_ClassificationOverride(t *testing.T) {
	op := 40
	tile := false
	ovs := []WatermarkOverride{{Classification: ClassConfidential, Enabled: true, Opacity: &op, Tile: &tile}}
	eff := ResolveWatermark(baseCfg(), ovs, ClassConfidential, false)
	if eff.Opacity != 40 || eff.Tile {
		t.Fatalf("override opacity/tile not applied: %+v", eff)
	}
}

func TestResolveWatermark_ForceOnWhenTenantDisabled(t *testing.T) {
	cfg := baseCfg()
	cfg.Enabled = false
	ovs := []WatermarkOverride{{Classification: ClassRestricted, Enabled: false, Force: true}}
	eff := ResolveWatermark(cfg, ovs, ClassRestricted, false)
	if !eff.Enabled {
		t.Fatal("force must enable even when tenant default is off")
	}
}

func TestResolveWatermark_PHIOverrideWins(t *testing.T) {
	op := 50
	ovs := []WatermarkOverride{
		{Classification: ClassInternal, Enabled: true, Opacity: intptr(10)},
		{Classification: WatermarkClassPHI, Enabled: true, Opacity: &op},
	}
	// doc is 'internal' but has PHI → the phi override wins.
	eff := ResolveWatermark(baseCfg(), ovs, ClassInternal, true)
	if eff.Opacity != 50 {
		t.Fatalf("phi override should win, got opacity %d", eff.Opacity)
	}
}

func TestSubstituteWatermarkTokens(t *testing.T) {
	got := SubstituteWatermarkTokens("{email} · {timestamp} · {ip}", map[string]string{
		"email": "alice@acme.com", "timestamp": "2026-07-01 14:03 UTC", "ip": "203.0.113.7",
	})
	want := "alice@acme.com · 2026-07-01 14:03 UTC · 203.0.113.7"
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

func TestWatermarkDownloadGated(t *testing.T) {
	cases := []struct {
		class string
		phi   bool
		gated bool
	}{
		{ClassUnclassified, false, false},
		{ClassInternal, false, false},
		{ClassConfidential, false, true},
		{ClassRestricted, false, true},
		{ClassInternal, true, true}, // PHI always gated
		{"", true, true},
	}
	for _, c := range cases {
		if got := WatermarkDownloadGated(c.class, c.phi); got != c.gated {
			t.Fatalf("class=%q phi=%v: want %v got %v", c.class, c.phi, c.gated, got)
		}
	}
}

func intptr(i int) *int { return &i }
