package auth

// knownRoles is every role an authenticated identity can carry:
//   - the tenant user roles allowed by users_role_check (document migration
//     000039_expand_user_roles): owner, admin, member, guest, viewer,
//     compliance_officer;
//   - api_key, the role APIKeyAuth stamps on a Bearer vdms_ caller (and the
//     role the MCP server forwards for its API-key callers).
//
// Keep it in step with users_role_check when a role is added there.
var knownRoles = map[string]struct{}{
	"owner":              {},
	"admin":              {},
	"member":             {},
	"guest":              {},
	"viewer":             {},
	"compliance_officer": {},
	"api_key":            {},
}

// IsKnownRole reports whether role is one of the roles above (exact,
// case-sensitive match). Used to validate a role a trusted internal caller
// names in X-User-Role — an unknown or empty role is never mapped to a
// default.
func IsKnownRole(role string) bool {
	_, ok := knownRoles[role]
	return ok
}
