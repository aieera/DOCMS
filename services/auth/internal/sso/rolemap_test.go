package sso

import "testing"

func TestResolveRole_RuleMatch(t *testing.T) {
	m := RoleMapping{Rules: []RoleRule{{ClaimValue: "Admins", Role: "admin"}}, DefaultRole: "member"}
	if got := ResolveRole(m, []string{"Users", "Admins"}); got != "admin" {
		t.Fatalf("expected admin, got %s", got)
	}
}

func TestResolveRole_CaseInsensitive(t *testing.T) {
	m := RoleMapping{Rules: []RoleRule{{ClaimValue: "Owners", Role: "owner"}}}
	if got := ResolveRole(m, []string{"owners"}); got != "owner" {
		t.Fatalf("expected owner (case-insensitive), got %s", got)
	}
}

func TestResolveRole_FirstRuleWins(t *testing.T) {
	m := RoleMapping{Rules: []RoleRule{
		{ClaimValue: "Staff", Role: "admin"},
		{ClaimValue: "Staff", Role: "member"},
	}}
	if got := ResolveRole(m, []string{"Staff"}); got != "admin" {
		t.Fatalf("expected first rule (admin) to win, got %s", got)
	}
}

func TestResolveRole_DefaultWhenNoMatch(t *testing.T) {
	m := RoleMapping{Rules: []RoleRule{{ClaimValue: "Admins", Role: "admin"}}, DefaultRole: "guest"}
	if got := ResolveRole(m, []string{"Nobody"}); got != "guest" {
		t.Fatalf("expected default guest, got %s", got)
	}
}

func TestResolveRole_MemberWhenNoDefault(t *testing.T) {
	if got := ResolveRole(RoleMapping{}, nil); got != "member" {
		t.Fatalf("expected member fallback, got %s", got)
	}
}

func TestResolveRole_InvalidRoleIgnored(t *testing.T) {
	// A rule with a bogus role is skipped; a bogus default falls to member.
	m := RoleMapping{Rules: []RoleRule{{ClaimValue: "X", Role: "superuser"}}, DefaultRole: "root"}
	if got := ResolveRole(m, []string{"X"}); got != "member" {
		t.Fatalf("expected member (invalid role/default), got %s", got)
	}
}

func TestRoleMappingFromConfig(t *testing.T) {
	raw := []byte(`{"issuer_url":"https://x","role_mapping":{"rules":[{"claim_value":"Admins","role":"admin"}],"default_role":"member"}}`)
	m := RoleMappingFromConfig(raw)
	if len(m.Rules) != 1 || m.Rules[0].Role != "admin" || m.DefaultRole != "member" {
		t.Fatalf("bad extraction: %+v", m)
	}
	// Malformed → zero mapping (caller then defaults to member).
	if got := ResolveRole(RoleMappingFromConfig([]byte("not json")), []string{"Admins"}); got != "member" {
		t.Fatalf("expected member on bad config, got %s", got)
	}
}
