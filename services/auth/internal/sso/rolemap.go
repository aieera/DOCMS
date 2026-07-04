package sso

import (
	"encoding/json"
	"strings"
)

// validRoles are the SeDoc roles a mapping may assign. Mirrors
// services/auth/internal/model.Role (kept as strings here to avoid an import
// cycle — the provisioner converts to model.Role).
var validRoles = map[string]bool{"owner": true, "admin": true, "member": true, "guest": true}

// ResolveRole picks a SeDoc role for a JIT user from the IdP's group/role
// claim values, using the mapping's ordered rules (first case-insensitive
// match wins). Falls back to DefaultRole, then "member". An unknown role in a
// rule/default is ignored (fails safe to "member").
func ResolveRole(m RoleMapping, groups []string) string {
	for _, rule := range m.Rules {
		if rule.ClaimValue == "" || !validRoles[rule.Role] {
			continue
		}
		for _, g := range groups {
			if strings.EqualFold(strings.TrimSpace(g), strings.TrimSpace(rule.ClaimValue)) {
				return rule.Role
			}
		}
	}
	if validRoles[m.DefaultRole] {
		return m.DefaultRole
	}
	return "member"
}

// RoleMappingFromConfig extracts the role_mapping from an sso_configs.config
// JSONB blob (works for both SAML and OIDC bodies). Returns the zero mapping
// on any parse error — the caller then defaults to "member".
func RoleMappingFromConfig(raw []byte) RoleMapping {
	var wrap struct {
		RoleMapping RoleMapping `json:"role_mapping"`
	}
	_ = json.Unmarshal(raw, &wrap)
	return wrap.RoleMapping
}
