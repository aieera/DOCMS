package model

// Wave 15.2 — geofence policy types.

import (
	"net/netip"
	"time"

	"github.com/google/uuid"
)

// GeofenceScope is the scope at which a policy applies.
type GeofenceScope string

const (
	ScopeTenant    GeofenceScope = "tenant"
	ScopeWorkspace GeofenceScope = "workspace"
	ScopeDocument  GeofenceScope = "document"
)

// GeofenceMode is the enforcement semantics.
type GeofenceMode string

const (
	ModeAllow  GeofenceMode = "allow"
	ModeDeny   GeofenceMode = "deny"
	ModeStepUp GeofenceMode = "step_up"
)

// GeofenceAction is the action class the policy applies to.
type GeofenceAction string

const (
	ActionRead  GeofenceAction = "read"
	ActionWrite GeofenceAction = "write"
	ActionAdmin GeofenceAction = "admin"
	ActionAny   GeofenceAction = "*"
)

// GeofencePolicy mirrors the `geofence_policies` row.
type GeofencePolicy struct {
	TenantID        uuid.UUID
	ID              uuid.UUID
	Scope           GeofenceScope
	ScopeID         *uuid.UUID // nil for scope=tenant
	Mode            GeofenceMode
	CountryCodes    []string        // ISO 3166-1 alpha-2, upper-cased
	CIDRAllowlist   []netip.Prefix
	CIDRDenylist    []netip.Prefix
	ApplyTo         GeofenceAction
	Enabled         bool
	CreatedByUserID *uuid.UUID
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// GeofenceDecision is what the middleware receives.
type GeofenceDecision struct {
	// Allow is false when the request must be rejected with 451.
	Allow bool
	// RequireStepUp is true when the request must satisfy step-up MFA
	// before the handler is invoked. Mutually exclusive with Allow=false.
	RequireStepUp bool
	// Reason is a short operator-readable string; surfaced in
	// metrics labels (low-cardinality) and in the 451/428 response
	// body. Never leaks IP or user data.
	Reason string
	// MatchedPolicyID identifies which policy produced the decision,
	// if any. Zero when no policy applied (default-allow).
	MatchedPolicyID uuid.UUID
}
