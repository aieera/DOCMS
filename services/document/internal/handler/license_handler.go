// license_handler — admin endpoint that reports the tenant's license
// state (ADR 0095 §13.7). Reads from pkg/license.Current() so the
// response reflects the JWT loaded at startup (or unlicensed_dev_mode
// if no JWT was supplied).
package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/aieera/sedoc/pkg/license"
)

// LicenseHandler exposes GET /api/v1/admin/tenant/license.
type LicenseHandler struct{}

func NewLicenseHandler() *LicenseHandler {
	return &LicenseHandler{}
}

// Register mounts the route. Caller wraps with SessionAuth + admin-role
// check at the mount site.
func (h *LicenseHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/tenant/license", h.get)
}

type licenseFeatureFlags struct {
	Esign      bool     `json:"esign"`
	MCP        bool     `json:"mcp"`
	IPaaS      bool     `json:"ipaas"`
	IntelLLM   bool     `json:"intel_llm"`
	Connectors []string `json:"connectors"`
	Regions    []string `json:"regions"`
}

type licenseResponse struct {
	Status         string               `json:"status"`
	TenantName     string               `json:"tenant_name,omitempty"`
	SeatLimit      *int                 `json:"seat_limit,omitempty"`
	SeatsUsed      *int                 `json:"seats_used,omitempty"`
	FeatureFlags   *licenseFeatureFlags `json:"feature_flags,omitempty"`
	Expiry         string               `json:"expiry,omitempty"`
	IssuedAt       string               `json:"issued_at,omitempty"`
	DaysRemaining  *int                 `json:"days_remaining,omitempty"`
	GraceDays      int                  `json:"grace_days"`
	IssuedTo       string               `json:"issued_to,omitempty"`
	IssuedBy       string               `json:"issued_by,omitempty"`
	ADR            string               `json:"adr"`
	EnforcementMsg string               `json:"enforcement_message"`
}

func (h *LicenseHandler) get(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	claims := license.Current()
	resp := licenseResponse{
		ADR: "0095",
	}

	if claims == nil {
		// Unlicensed dev mode — no JWT loaded.
		resp.Status = string(license.StatusUnlicensedDev)
		resp.GraceDays = 30
		resp.EnforcementMsg = "Running unlicensed. No license JWT loaded; every feature implicitly enabled. " +
			"Set SEDOC_LICENSE_JWT (or place a license at /etc/vaultdms/license.jwt) to activate enforcement. " +
			"See ADR 0095 for the design plan."
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	// Licensed — fill the real claims.
	status := claims.DerivedStatus(now)
	days := claims.DaysRemaining(now)

	resp.Status = string(status)
	resp.TenantName = claims.TenantName
	resp.SeatLimit = ptrInt(claims.SeatLimit)
	resp.FeatureFlags = &licenseFeatureFlags{
		Esign:      claims.FeatureFlags.Esign,
		MCP:        claims.FeatureFlags.MCP,
		IPaaS:      claims.FeatureFlags.IPaaS,
		IntelLLM:   claims.FeatureFlags.IntelLLM,
		Connectors: claims.FeatureFlags.Connectors,
		Regions:    claims.FeatureFlags.Regions,
	}
	resp.Expiry = claims.ExpiresAt.UTC().Format(time.RFC3339)
	if !claims.IssuedAt.IsZero() {
		resp.IssuedAt = claims.IssuedAt.UTC().Format(time.RFC3339)
	}
	resp.DaysRemaining = ptrInt(days)
	resp.GraceDays = claims.GraceDays
	resp.IssuedTo = claims.IssuedTo
	resp.IssuedBy = claims.IssuedBy

	switch status {
	case license.StatusActive:
		resp.EnforcementMsg = "License active. JWT verified at startup; re-validated hourly."
	case license.StatusGrace:
		resp.EnforcementMsg = "License is in grace period. Writes are gated (HTTP 423 Locked); reads continue. " +
			"Renew before the grace window ends."
	case license.StatusExpired:
		resp.EnforcementMsg = "License expired past the grace window. Services will refuse to start " +
			"when SEDOC_REQUIRE_LICENSE=true. Contact BD ops for a new license."
	}

	// seats_used is intentionally omitted in this phase — counting active
	// users requires the auth service's users table and a tenant-scoped
	// query that doesn't fit document service. Phase 4 (ADR 0095) adds
	// the 5-min background job that populates it.

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func ptrInt(v int) *int { return &v }
