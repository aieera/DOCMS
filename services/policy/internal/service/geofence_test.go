package service

import (
	"net"
	"net/netip"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/vaultdms/vaultdms/services/policy/internal/model"
)

func mustPrefix(t *testing.T, s string) netip.Prefix {
	t.Helper()
	p, err := netip.ParsePrefix(s)
	require.NoError(t, err)
	return p
}

func TestDecide_DefaultAllowWhenNoPolicies(t *testing.T) {
	d := decide(nil, model.ActionRead, net.ParseIP("203.0.113.5"), "US")
	require.True(t, d.Allow)
	require.False(t, d.RequireStepUp)
	require.Equal(t, "no_policy", d.Reason)
}

func TestDecide_CIDRDenylistWins(t *testing.T) {
	pid := uuid.New()
	policies := []model.GeofencePolicy{{
		ID:           pid,
		Scope:        model.ScopeTenant,
		Mode:         model.ModeDeny,
		CIDRDenylist: []netip.Prefix{mustPrefix(t, "203.0.113.0/24")},
		ApplyTo:      model.ActionAny,
	}}
	d := decide(policies, model.ActionRead, net.ParseIP("203.0.113.5"), "US")
	require.False(t, d.Allow)
	require.Equal(t, "cidr_denylist", d.Reason)
	require.Equal(t, pid, d.MatchedPolicyID)
}

func TestDecide_CountryDenyBlocks(t *testing.T) {
	policies := []model.GeofencePolicy{{
		Scope:        model.ScopeWorkspace,
		Mode:         model.ModeDeny,
		CountryCodes: []string{"CN"},
		ApplyTo:      model.ActionAny,
	}}
	d := decide(policies, model.ActionRead, net.ParseIP("1.2.3.4"), "CN")
	require.False(t, d.Allow)
	require.Equal(t, "country_deny", d.Reason)
}

func TestDecide_AllowCountryListDeniesOthers(t *testing.T) {
	policies := []model.GeofencePolicy{{
		Scope:        model.ScopeTenant,
		Mode:         model.ModeAllow,
		CountryCodes: []string{"US", "CA"},
		ApplyTo:      model.ActionAny,
	}}
	// Matching country → allow.
	d := decide(policies, model.ActionRead, net.ParseIP("1.2.3.4"), "US")
	require.True(t, d.Allow)

	// Non-matching → deny.
	d = decide(policies, model.ActionRead, net.ParseIP("1.2.3.4"), "DE")
	require.False(t, d.Allow)
	require.Equal(t, "country_not_in_allowlist", d.Reason)

	// Unknown country (resolver failed) → deny, same policy.
	d = decide(policies, model.ActionRead, net.ParseIP("1.2.3.4"), "")
	require.False(t, d.Allow)
}

func TestDecide_StepUpRequired(t *testing.T) {
	policies := []model.GeofencePolicy{{
		Scope:        model.ScopeDocument,
		Mode:         model.ModeStepUp,
		CountryCodes: []string{"RU"},
		ApplyTo:      model.ActionAny,
	}}
	d := decide(policies, model.ActionWrite, net.ParseIP("1.2.3.4"), "RU")
	require.True(t, d.Allow)
	require.True(t, d.RequireStepUp)
	require.Equal(t, "step_up_required", d.Reason)
}

func TestDecide_ActionFilter(t *testing.T) {
	policies := []model.GeofencePolicy{{
		Scope:        model.ScopeTenant,
		Mode:         model.ModeDeny,
		CountryCodes: []string{"CN"},
		ApplyTo:      model.ActionWrite,
	}}
	// read request — policy doesn't apply.
	d := decide(policies, model.ActionRead, net.ParseIP("1.2.3.4"), "CN")
	require.True(t, d.Allow)

	// write request — policy fires.
	d = decide(policies, model.ActionWrite, net.ParseIP("1.2.3.4"), "CN")
	require.False(t, d.Allow)
}

func TestDecide_CIDRAllowlistMatches(t *testing.T) {
	policies := []model.GeofencePolicy{{
		Scope:         model.ScopeTenant,
		Mode:          model.ModeAllow,
		CIDRAllowlist: []netip.Prefix{mustPrefix(t, "10.0.0.0/8")},
		ApplyTo:       model.ActionAny,
	}}
	d := decide(policies, model.ActionRead, net.ParseIP("10.1.2.3"), "")
	require.True(t, d.Allow)
	require.Equal(t, "cidr_allowlist", d.Reason)
}
