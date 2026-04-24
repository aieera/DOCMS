// Package middleware provides the HTTP enforcement layer for Wave 15.2
// geofencing. The middleware wraps a Decider interface so tests — and
// the policy service wiring itself — can swap in a mock.
//
// Responses:
//   deny           → 451 Unavailable For Legal Reasons, empty body
//   step-up        → 428 Precondition Required, WWW-Authenticate: Step-Up
//   allow          → next.ServeHTTP
//
// Source IP is read via realClientIP (X-Forwarded-For right-most
// untrusted hop, falling back to r.RemoteAddr). The trusted-proxy
// chain is honoured by pkg/gateway/waf.go's RealIP middleware that
// runs earlier in the chain; this middleware is safe to compose
// after it.
//
// NOTE: this package MUST NOT import services/policy. The Decider
// interface keeps the policy-service binary wire-compatible without
// creating a reverse dependency. The concrete implementation lives in
// services/policy/internal/service/geofence.go (method Decide).
package middleware

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/vaultdms/vaultdms/pkg/auth"
)

// GeofenceAction mirrors services/policy/internal/model.GeofenceAction
// without the cross-module import. Keep values in sync.
type GeofenceAction string

const (
	GeofenceActionRead  GeofenceAction = "read"
	GeofenceActionWrite GeofenceAction = "write"
	GeofenceActionAdmin GeofenceAction = "admin"
	GeofenceActionAny   GeofenceAction = "*"
)

// GeofenceDecision is the middleware-facing decision shape — mirrors
// services/policy/internal/model.GeofenceDecision.
type GeofenceDecision struct {
	Allow           bool
	RequireStepUp   bool
	Reason          string
	MatchedPolicyID uuid.UUID
}

// GeofenceDeciderFn decides per-request. The policy service's
// *GeofenceService implements the semantic contract and is adapted at
// wire-up by a 3-line closure.
type GeofenceDeciderFn func(ctx context.Context, tenantID uuid.UUID, workspaceID, documentID *uuid.UUID, action GeofenceAction, ip net.IP) (GeofenceDecision, error)

// GeofenceConfig wires the middleware.
type GeofenceConfig struct {
	Decide GeofenceDeciderFn

	// Action translates the HTTP method into a geofence action. If
	// nil, the default (GET/HEAD→read, others→write) is used.
	Action func(r *http.Request) GeofenceAction

	// ResolveTenant extracts the tenant from context — default
	// reads pkg/tenant.IDFrom. Swap in tests.
	ResolveTenant func(r *http.Request) (uuid.UUID, bool)

	// ResolveScope returns optional workspace / document ids from
	// the URL. Default returns (nil, nil).
	ResolveScope func(r *http.Request) (workspaceID, documentID *uuid.UUID)

	// StepUpHook is called when a step-up decision fires; default
	// sets WWW-Authenticate: Step-Up and returns 428. Override when
	// the service mounts a custom MFA challenge flow.
	StepUpHook func(w http.ResponseWriter, r *http.Request, d GeofenceDecision)

	// OnDecision is a metrics hook (tenant, mode, result) →
	// increment. Default is no-op. Pass the policy service's
	// ObserveDecision method.
	OnDecision func(tenant string, mode string, result string)
}

// Geofence returns an http middleware that enforces the supplied
// decider. Ordering: this middleware MUST run AFTER tenant
// resolution (pkg/middleware/tenant.go) and BEFORE the handler.
func Geofence(cfg GeofenceConfig) func(http.Handler) http.Handler {
	if cfg.Decide == nil {
		// Fail-open with an explicit panic at wire-up time so a
		// mis-configured service never silently serves all traffic
		// past a geofence gate in prod. Detected at startup, not
		// at first request.
		panic("middleware.Geofence: Decide is required")
	}
	if cfg.Action == nil {
		cfg.Action = defaultAction
	}
	if cfg.ResolveTenant == nil {
		cfg.ResolveTenant = func(r *http.Request) (uuid.UUID, bool) {
			id, err := auth.GetTenantID(r.Context())
			if err != nil || id == uuid.Nil {
				return uuid.Nil, false
			}
			return id, true
		}
	}
	if cfg.ResolveScope == nil {
		cfg.ResolveScope = func(*http.Request) (*uuid.UUID, *uuid.UUID) { return nil, nil }
	}
	if cfg.StepUpHook == nil {
		cfg.StepUpHook = defaultStepUpHook
	}
	if cfg.OnDecision == nil {
		cfg.OnDecision = func(string, string, string) {}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenantID, ok := cfg.ResolveTenant(r)
			if !ok {
				// No tenant → let downstream auth reject. Geofence
				// is tenant-scoped only.
				next.ServeHTTP(w, r)
				return
			}
			ip := realClientIP(r)
			wsID, docID := cfg.ResolveScope(r)
			action := cfg.Action(r)
			decision, err := cfg.Decide(r.Context(), tenantID, wsID, docID, action, ip)
			if err != nil {
				// Fail CLOSED on decider errors — a silent allow
				// defeats the purpose of the control. 503 so the
				// caller retries rather than assuming the resource
				// is permanently unavailable.
				cfg.OnDecision(tenantID.String(), "error", "error")
				http.Error(w, "geofence check failed", http.StatusServiceUnavailable)
				return
			}
			if !decision.Allow {
				cfg.OnDecision(tenantID.String(), "deny", decision.Reason)
				w.Header().Set("X-Geofence-Reason", decision.Reason)
				w.WriteHeader(http.StatusUnavailableForLegalReasons) // 451
				return
			}
			if decision.RequireStepUp {
				cfg.OnDecision(tenantID.String(), "step_up", decision.Reason)
				cfg.StepUpHook(w, r, decision)
				return
			}
			cfg.OnDecision(tenantID.String(), "allow", decision.Reason)
			next.ServeHTTP(w, r)
		})
	}
}

func defaultAction(r *http.Request) GeofenceAction {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return GeofenceActionRead
	default:
		return GeofenceActionWrite
	}
}

func defaultStepUpHook(w http.ResponseWriter, _ *http.Request, d GeofenceDecision) {
	w.Header().Set("WWW-Authenticate", "Step-Up")
	w.Header().Set("X-Geofence-Reason", d.Reason)
	w.WriteHeader(http.StatusPreconditionRequired) // 428
}

// realClientIP extracts the source IP. Upstream pkg/http/realip or
// chi's RealIP middleware set r.RemoteAddr to the un-proxied client
// IP; we defensively re-derive from X-Forwarded-For in case this
// middleware is mounted standalone.
func realClientIP(r *http.Request) net.IP {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Right-most hop is the one added by the trusted proxy.
		parts := strings.Split(xff, ",")
		candidate := strings.TrimSpace(parts[len(parts)-1])
		if ip := net.ParseIP(candidate); ip != nil {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}
