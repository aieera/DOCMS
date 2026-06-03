// Package license — JWT-based license validation for VaultDMS (ADR 0095).
//
// A license is an RS256-signed JWT minted by the license-gen tool (held
// by BD ops). Every service loads its license at startup from
// SEDOC_LICENSE_JWT (env) or /etc/vaultdms/license.jwt (file),
// verifies against the bundled public key, and caches the parsed claims.
// A background goroutine re-validates every hour so expiry transitions
// (active → grace → expired) take effect without a restart.
//
// Today only the document service consumes this package via its
// admin/tenant/license handler. The wire-up pattern is mechanical and
// the remaining services will adopt it in a follow-up (ADR 0095 §
// Phased rollout, Phase 2).
package license

import "time"

// FeatureFlags mirrors the JWT `feature_flags` claim. Each per-service
// gate calls Has*() with a feature key; unset features default to
// disabled in licensed mode and to enabled in unlicensed-dev mode
// (today's behavior).
type FeatureFlags struct {
	Esign      bool     `json:"esign,omitempty"`
	MCP        bool     `json:"mcp,omitempty"`
	IPaaS      bool     `json:"ipaas,omitempty"`
	IntelLLM   bool     `json:"intel_llm,omitempty"`
	Connectors []string `json:"connectors,omitempty"`
	Regions    []string `json:"regions,omitempty"`
}

// Claims is the validated, parsed license payload.
type Claims struct {
	// Standard JWT registered claims (subset)
	Issuer    string    `json:"iss"`
	Subject   string    `json:"sub"`
	IssuedAt  time.Time `json:"iat"`
	ExpiresAt time.Time `json:"exp"`

	// VaultDMS-specific claims
	TenantName   string       `json:"tenant_name"`
	SeatLimit    int          `json:"seat_limit"`
	FeatureFlags FeatureFlags `json:"feature_flags"`
	IssuedTo     string       `json:"issued_to"`
	IssuedBy     string       `json:"issued_by"`
	GraceDays    int          `json:"grace_days"`
}

// Status — derived from `now()` vs `ExpiresAt + GraceDays`. Drives
// the read-only mode banner and write-gate middleware.
type Status string

const (
	// StatusUnlicensedDev — no JWT loaded. Today's default. Every
	// feature implicitly enabled; nothing enforced.
	StatusUnlicensedDev Status = "unlicensed_dev_mode"

	// StatusActive — JWT present, valid signature, not expired.
	StatusActive Status = "active"

	// StatusGrace — expired ≤ GraceDays ago. Reads allowed; writes
	// gated at the middleware layer with HTTP 423 Locked.
	StatusGrace Status = "grace"

	// StatusExpired — expired more than GraceDays ago. Services
	// refuse to start with this status when SEDOC_REQUIRE_LICENSE=true.
	StatusExpired Status = "expired"
)

// DerivedStatus returns Status given now(). Caller decides whether
// to allow writes / refuse to start based on this.
func (c *Claims) DerivedStatus(now time.Time) Status {
	if c == nil {
		return StatusUnlicensedDev
	}
	if now.Before(c.ExpiresAt) {
		return StatusActive
	}
	graceEnd := c.ExpiresAt.Add(time.Duration(c.GraceDays) * 24 * time.Hour)
	if now.Before(graceEnd) {
		return StatusGrace
	}
	return StatusExpired
}

// DaysRemaining is positive when the license is active, zero or
// negative when in grace/expired. Frontend uses this for the
// countdown banner (60/30/7-day thresholds per the blueprint).
func (c *Claims) DaysRemaining(now time.Time) int {
	if c == nil {
		return 0
	}
	delta := c.ExpiresAt.Sub(now)
	return int(delta.Hours() / 24)
}

// HasFeature is a thin helper for the boolean flags. Connector and
// region lists have their own helpers below.
func (c *Claims) HasFeature(name string) bool {
	if c == nil {
		// Unlicensed dev mode: everything implicitly enabled. This
		// matches today's behavior so adopting pkg/license doesn't
		// regress dev workflows.
		return true
	}
	switch name {
	case "esign":
		return c.FeatureFlags.Esign
	case "mcp":
		return c.FeatureFlags.MCP
	case "ipaas":
		return c.FeatureFlags.IPaaS
	case "intel_llm":
		return c.FeatureFlags.IntelLLM
	}
	return false
}

// AllowsConnector returns true if the named provider is in the
// FeatureFlags.Connectors allow-list (or if running unlicensed).
func (c *Claims) AllowsConnector(name string) bool {
	if c == nil {
		return true
	}
	for _, p := range c.FeatureFlags.Connectors {
		if p == name {
			return true
		}
	}
	return false
}

// AllowsRegion — same shape, for upload region pinning.
func (c *Claims) AllowsRegion(name string) bool {
	if c == nil {
		return true
	}
	for _, r := range c.FeatureFlags.Regions {
		if r == name {
			return true
		}
	}
	return false
}
