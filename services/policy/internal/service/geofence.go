package service

// Wave 15.2 — geofence decision + CRUD.
//
// The decision engine is deliberately Go-native (not a rego module)
// for latency reasons: it runs on every authenticated request. The
// equivalent rego rule is mirrored in opa/policy.rego as a test
// fixture so operators can author portable policies elsewhere.
//
// Precedence (first match wins within a scope; scopes are evaluated
// outer→inner, tenant → workspace → document):
//   1. cidr_denylist  → deny
//   2. cidr_allowlist → allow (short-circuits downstream policies in
//                              the same scope)
//   3. country in deny set + country in allow set → deny
//   4. country in allow set (when country list is the allow policy)
//   5. mode=step_up + any country/CIDR match → require step-up
//
// If no policy matches, the decision is allow. Unknown country
// (resolver returned ErrUnknown) is treated as "no country match"
// and, combined with a country-only allow policy, means the request
// is denied — the safer default for "lock to EU" tenants.

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vaultdms/vaultdms/pkg/database"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/pkg/geo"
	"github.com/vaultdms/vaultdms/services/policy/internal/model"
	"github.com/vaultdms/vaultdms/services/policy/internal/repository"
)

// GeofenceDecider is the read side of the service — resolves per-request.
type GeofenceDecider interface {
	Decide(ctx context.Context, in GeofenceDecisionInput) (model.GeofenceDecision, error)
}

// GeofenceDecisionInput is the minimum needed to produce a decision.
// WorkspaceID and DocumentID are optional — when set, their
// scope-level policies are consulted.
type GeofenceDecisionInput struct {
	TenantID    uuid.UUID
	WorkspaceID *uuid.UUID
	DocumentID  *uuid.UUID
	Action      model.GeofenceAction
	SourceIP    net.IP
}

// GeofenceService bundles CRUD + Decide. Constructed once per process;
// safe for concurrent use.
type GeofenceService struct {
	repo     repository.GeofenceRepo
	resolver geo.Resolver
	pool     *pgxpool.Pool
}

// NewGeofenceService wires the service. resolver may be nil to mean
// "no geo resolution available" — country policies still compile but
// every country match falls through as if it were "unknown".
func NewGeofenceService(pool *pgxpool.Pool, repo repository.GeofenceRepo, resolver geo.Resolver) *GeofenceService {
	return &GeofenceService{pool: pool, repo: repo, resolver: resolver}
}

// ---- CRUD -----------------------------------------------------------------

// CreateGeofenceInput is the validated shape the handler passes in.
type CreateGeofenceInput struct {
	TenantID        uuid.UUID
	Scope           model.GeofenceScope
	ScopeID         *uuid.UUID
	Mode            model.GeofenceMode
	CountryCodes    []string
	CIDRAllowlist   []netip.Prefix
	CIDRDenylist    []netip.Prefix
	ApplyTo         model.GeofenceAction
	Enabled         bool
	CreatedByUserID uuid.UUID
}

// Create inserts a new geofence policy.
func (s *GeofenceService) Create(ctx context.Context, in CreateGeofenceInput) (*model.GeofencePolicy, error) {
	if err := validateGeofenceInput(in.Scope, in.ScopeID, in.Mode, in.CountryCodes, in.CIDRAllowlist, in.CIDRDenylist, in.ApplyTo); err != nil {
		return nil, err
	}
	p := &model.GeofencePolicy{
		TenantID:        in.TenantID,
		ID:              uuid.New(),
		Scope:           in.Scope,
		ScopeID:         in.ScopeID,
		Mode:            in.Mode,
		CountryCodes:    normalizeCountries(in.CountryCodes),
		CIDRAllowlist:   in.CIDRAllowlist,
		CIDRDenylist:    in.CIDRDenylist,
		ApplyTo:         in.ApplyTo,
		Enabled:         in.Enabled,
		CreatedByUserID: &in.CreatedByUserID,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}
	if err := database.WithTenantTx(ctx, s.pool, in.TenantID, func(tx pgx.Tx) error {
		return s.repo.Insert(ctx, tx, p)
	}); err != nil {
		return nil, err
	}
	return p, nil
}

// List returns every policy in the tenant.
func (s *GeofenceService) List(ctx context.Context, tenantID uuid.UUID) ([]model.GeofencePolicy, error) {
	var out []model.GeofencePolicy
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		policies, err := s.repo.List(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		out = policies
		return nil
	})
	return out, err
}

// Delete removes a policy.
func (s *GeofenceService) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return s.repo.Delete(ctx, tx, tenantID, id)
	})
}

// ---- Decide ---------------------------------------------------------------

// Decide evaluates the applicable policies for this request. Any
// error from the underlying DB fails closed — the caller must
// translate to 5xx and NOT let the request through.
func (s *GeofenceService) Decide(ctx context.Context, in GeofenceDecisionInput) (model.GeofenceDecision, error) {
	scopes := []model.GeofenceScope{model.ScopeTenant}
	var scopeIDs []uuid.UUID
	if in.WorkspaceID != nil {
		scopes = append(scopes, model.ScopeWorkspace)
		scopeIDs = append(scopeIDs, *in.WorkspaceID)
	}
	if in.DocumentID != nil {
		scopes = append(scopes, model.ScopeDocument)
		scopeIDs = append(scopeIDs, *in.DocumentID)
	}

	var policies []model.GeofencePolicy
	if err := database.WithTenantTx(ctx, s.pool, in.TenantID, func(tx pgx.Tx) error {
		p, err := s.repo.ListForScope(ctx, tx, in.TenantID, scopes, scopeIDs)
		if err != nil {
			return err
		}
		policies = p
		return nil
	}); err != nil {
		return model.GeofenceDecision{}, err
	}

	country := ""
	if s.resolver != nil && in.SourceIP != nil {
		cc, err := s.resolver.LookupCountry(in.SourceIP)
		if err == nil {
			country = cc
		}
	}

	return decide(policies, in.Action, in.SourceIP, country), nil
}

// decide is the pure-function core — exported to tests via the
// package-level decide symbol. Returns allow by default.
func decide(policies []model.GeofencePolicy, action model.GeofenceAction, ip net.IP, country string) model.GeofenceDecision {
	var stepUpMatch *model.GeofencePolicy

	for i := range policies {
		p := policies[i]
		if !appliesToAction(p.ApplyTo, action) {
			continue
		}

		// CIDR denylist always wins, regardless of mode.
		if ip != nil && prefixListContains(p.CIDRDenylist, ip) {
			return model.GeofenceDecision{
				Allow:           false,
				Reason:          "cidr_denylist",
				MatchedPolicyID: p.ID,
			}
		}

		switch p.Mode {
		case model.ModeDeny:
			if country != "" && containsFold(p.CountryCodes, country) {
				return model.GeofenceDecision{
					Allow:           false,
					Reason:          "country_deny",
					MatchedPolicyID: p.ID,
				}
			}
		case model.ModeAllow:
			// allow mode requires an affirmative match against either
			// the CIDR allowlist or the country list.
			if ip != nil && prefixListContains(p.CIDRAllowlist, ip) {
				return model.GeofenceDecision{Allow: true, Reason: "cidr_allowlist", MatchedPolicyID: p.ID}
			}
			if len(p.CountryCodes) > 0 {
				if country == "" || !containsFold(p.CountryCodes, country) {
					return model.GeofenceDecision{
						Allow:           false,
						Reason:          "country_not_in_allowlist",
						MatchedPolicyID: p.ID,
					}
				}
			} else if len(p.CIDRAllowlist) > 0 {
				// CIDR-only allow list and no match → deny.
				return model.GeofenceDecision{
					Allow:           false,
					Reason:          "ip_not_in_cidr_allowlist",
					MatchedPolicyID: p.ID,
				}
			}
		case model.ModeStepUp:
			matched := false
			if country != "" && containsFold(p.CountryCodes, country) {
				matched = true
			}
			if !matched && ip != nil && prefixListContains(p.CIDRDenylist, ip) {
				matched = true
			}
			if matched {
				// Only the strictest step-up wins — remember the first.
				if stepUpMatch == nil {
					copy := p
					stepUpMatch = &copy
				}
			}
		}
	}

	if stepUpMatch != nil {
		return model.GeofenceDecision{
			Allow:           true,
			RequireStepUp:   true,
			Reason:          "step_up_required",
			MatchedPolicyID: stepUpMatch.ID,
		}
	}
	return model.GeofenceDecision{Allow: true, Reason: "no_policy"}
}

// ---- helpers --------------------------------------------------------------

func appliesToAction(p model.GeofenceAction, a model.GeofenceAction) bool {
	if p == model.ActionAny || p == "" {
		return true
	}
	return p == a
}

func prefixListContains(prefixes []netip.Prefix, ip net.IP) bool {
	if len(prefixes) == 0 || ip == nil {
		return false
	}
	addr, ok := netip.AddrFromSlice(ip.To16())
	if !ok {
		return false
	}
	// Unmap IPv4-in-v6 so prefix.Contains works for both families.
	addr = addr.Unmap()
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func containsFold(set []string, target string) bool {
	for _, s := range set {
		if len(s) == len(target) && equalFoldAscii(s, target) {
			return true
		}
	}
	return false
}

// equalFoldAscii is a tight ASCII-only fold; country codes are always
// 2 ASCII letters so strings.EqualFold is overkill here.
func equalFoldAscii(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'a' && ca <= 'z' {
			ca -= 'a' - 'A'
		}
		if cb >= 'a' && cb <= 'z' {
			cb -= 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func normalizeCountries(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, cc := range in {
		if len(cc) != 2 {
			continue
		}
		ua := cc
		// Upper-case ASCII in place.
		b := []byte(ua)
		for i := range b {
			if b[i] >= 'a' && b[i] <= 'z' {
				b[i] -= 'a' - 'A'
			}
		}
		out = append(out, string(b))
	}
	return out
}

// validateGeofenceInput runs the invariants also enforced by the DB
// constraint — early so the admin UI gets a clear 400 instead of a
// driver-level error.
func validateGeofenceInput(scope model.GeofenceScope, scopeID *uuid.UUID, mode model.GeofenceMode, countries []string, allowlist, denylist []netip.Prefix, action model.GeofenceAction) error {
	switch scope {
	case model.ScopeTenant:
		if scopeID != nil {
			return vdmserr.Validation("scope_id", "must be omitted for scope=tenant")
		}
	case model.ScopeWorkspace, model.ScopeDocument:
		if scopeID == nil {
			return vdmserr.Validation("scope_id", "required for scope=workspace|document")
		}
	default:
		return vdmserr.Validation("scope", "must be tenant|workspace|document")
	}
	switch mode {
	case model.ModeAllow, model.ModeDeny, model.ModeStepUp:
	default:
		return vdmserr.Validation("mode", "must be allow|deny|step_up")
	}
	if len(countries) == 0 && len(allowlist) == 0 && len(denylist) == 0 {
		return vdmserr.Validation("policy", "must specify at least one of country_codes, cidr_allowlist, cidr_denylist")
	}
	for _, cc := range countries {
		if len(cc) != 2 {
			return vdmserr.Validation("country_codes", "must be ISO 3166-1 alpha-2")
		}
	}
	switch action {
	case model.ActionRead, model.ActionWrite, model.ActionAdmin, model.ActionAny, "":
	default:
		return vdmserr.Validation("apply_to", "must be read|write|admin|*")
	}
	return nil
}

// ErrUnknownCountry is re-exported for middleware decisions.
var ErrUnknownCountry = errors.New("geofence: country unknown")
