package model

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// WatermarkClassPHI is the pseudo-classification matching documents flagged
// has_phi. It takes precedence over the security_classification level when
// resolving overrides.
const WatermarkClassPHI = "phi"

// WatermarkConfig is the per-tenant default watermark style (§5).
type WatermarkConfig struct {
	Enabled     bool
	Template    string
	Opacity     int
	RotationDeg int
	Tile        bool
	FontSize    int
	Color       string
}

// DefaultWatermarkConfig is returned when a tenant has no row yet (disabled).
func DefaultWatermarkConfig() WatermarkConfig {
	return WatermarkConfig{
		Enabled:     false,
		Template:    "{email} · {timestamp} · {ip}",
		Opacity:     15,
		RotationDeg: 30,
		Tile:        true,
		FontSize:    28,
		Color:       "#808080",
	}
}

// WatermarkOverride tunes the watermark for one classification. Opacity/Tile
// are nil-to-inherit; Force turns the watermark on even if the tenant default
// is off.
type WatermarkOverride struct {
	ID             uuid.UUID
	Classification string
	Enabled        bool
	Opacity        *int
	Tile           *bool
	Force          bool
	Description    string
	CreatedAt      time.Time
	CreatedBy      uuid.UUID
}

// EffectiveWatermark is the resolved style for a specific document, before
// identity-token substitution.
type EffectiveWatermark struct {
	Enabled     bool
	Template    string
	Opacity     int
	RotationDeg int
	Tile        bool
	FontSize    int
	Color       string
}

// ResolveWatermark applies the matching per-classification override onto the
// tenant config. A 'phi' override wins when hasPHI; otherwise the override
// matching securityClassification (if any) applies. Overrides only tune
// enabled/opacity/tile/force — template, rotation, font, and color come from
// the tenant config so the look stays consistent.
func ResolveWatermark(cfg WatermarkConfig, overrides []WatermarkOverride, securityClassification string, hasPHI bool) EffectiveWatermark {
	eff := EffectiveWatermark{
		Enabled:     cfg.Enabled,
		Template:    cfg.Template,
		Opacity:     cfg.Opacity,
		RotationDeg: cfg.RotationDeg,
		Tile:        cfg.Tile,
		FontSize:    cfg.FontSize,
		Color:       cfg.Color,
	}
	var ov *WatermarkOverride
	if hasPHI {
		ov = findWatermarkOverride(overrides, WatermarkClassPHI)
	}
	if ov == nil && securityClassification != "" {
		ov = findWatermarkOverride(overrides, securityClassification)
	}
	if ov != nil {
		eff.Enabled = ov.Enabled
		if ov.Force {
			eff.Enabled = true
		}
		if ov.Opacity != nil {
			eff.Opacity = *ov.Opacity
		}
		if ov.Tile != nil {
			eff.Tile = *ov.Tile
		}
	}
	return eff
}

func findWatermarkOverride(overrides []WatermarkOverride, class string) *WatermarkOverride {
	for i := range overrides {
		if overrides[i].Classification == class {
			return &overrides[i]
		}
	}
	return nil
}

// SubstituteWatermarkTokens fills the template's {token} placeholders with the
// viewer's identity values. Unknown tokens are left untouched; a missing value
// substitutes empty.
func SubstituteWatermarkTokens(template string, tokens map[string]string) string {
	out := template
	for k, v := range tokens {
		out = strings.ReplaceAll(out, "{"+k+"}", v)
	}
	return out
}

// WatermarkDownloadGated reports whether the download/print path should burn in
// the watermark for a document of this sensitivity — the "where required" gate
// (confidential/restricted level, or any PHI). Low-sensitivity downloads stay a
// cheap passthrough.
func WatermarkDownloadGated(securityClassification string, hasPHI bool) bool {
	if hasPHI {
		return true
	}
	switch securityClassification {
	case ClassConfidential, ClassRestricted:
		return true
	}
	return false
}
