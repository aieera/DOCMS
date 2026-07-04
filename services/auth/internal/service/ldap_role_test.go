package service

import (
	"testing"

	"github.com/aieera/sedoc/services/auth/internal/model"
	"github.com/aieera/sedoc/services/auth/internal/repository"
)

func m(dn, role string) repository.LDAPGroupMappingRow {
	return repository.LDAPGroupMappingRow{LDAPGroupDN: dn, DMSRole: role}
}

func TestResolveRoleFromMappings_Match(t *testing.T) {
	maps := []repository.LDAPGroupMappingRow{m("CN=Admins,OU=Groups,DC=x", "admin")}
	role, matched := resolveRoleFromMappings(maps, []string{"CN=Users,OU=Groups,DC=x", "CN=Admins,OU=Groups,DC=x"})
	if !matched || role != model.RoleAdmin {
		t.Fatalf("expected admin/matched, got %s/%v", role, matched)
	}
}

func TestResolveRoleFromMappings_CaseInsensitive(t *testing.T) {
	maps := []repository.LDAPGroupMappingRow{m("CN=Owners,DC=x", "owner")}
	role, matched := resolveRoleFromMappings(maps, []string{"cn=owners,dc=x"})
	if !matched || role != model.RoleOwner {
		t.Fatalf("expected owner (case-insensitive), got %s/%v", role, matched)
	}
}

func TestResolveRoleFromMappings_HighestPrecedenceWins(t *testing.T) {
	maps := []repository.LDAPGroupMappingRow{m("CN=A,DC=x", "member"), m("CN=B,DC=x", "admin")}
	role, _ := resolveRoleFromMappings(maps, []string{"CN=A,DC=x", "CN=B,DC=x"})
	if role != model.RoleAdmin {
		t.Fatalf("expected admin (highest precedence), got %s", role)
	}
}

func TestResolveRoleFromMappings_NoMatchNotApplied(t *testing.T) {
	maps := []repository.LDAPGroupMappingRow{m("CN=Admins,DC=x", "admin")}
	role, matched := resolveRoleFromMappings(maps, []string{"CN=Nobody,DC=x"})
	if matched {
		t.Fatal("expected no match")
	}
	if role != model.RoleMember {
		t.Fatalf("expected member default, got %s", role)
	}
}

func TestResolveRoleFromMappings_MappingWithoutRoleIgnored(t *testing.T) {
	// A group-only mapping (no role) must not contribute a role.
	maps := []repository.LDAPGroupMappingRow{m("CN=Staff,DC=x", "")}
	_, matched := resolveRoleFromMappings(maps, []string{"CN=Staff,DC=x"})
	if matched {
		t.Fatal("group-only mapping should not match a role")
	}
}
