// Package auth — *http.Request flavored convenience wrappers.
//
// HTTP handlers across services kept growing private tenantFromCtx /
// userFromCtx / roleFromCtx helpers that each did exactly the same
// thing: pull the identity field off r.Context() and stringify it.
// Three byte-identical copies grew across audit/connector/search.
// These shared wrappers replace them so the next service doesn't
// invent a fourth copy.
//
// Stringified returns ("" on missing) match the call-site convention
// — handlers feed these straight into log fields, query parameters,
// and error envelopes where a uuid.UUID would need .String() anyway.

package auth

import "net/http"

// TenantIDString returns the tenant UUID as a string, or "" when the
// request context carries no tenant. Convenience wrapper around
// GetTenantID for handlers that already have *http.Request.
func TenantIDString(r *http.Request) string {
	id, err := GetTenantID(r.Context())
	if err != nil {
		return ""
	}
	return id.String()
}

// UserIDString returns the authenticated user's UUID as a string, or
// "" when the request context carries no user. Convenience wrapper
// around GetUserID.
func UserIDString(r *http.Request) string {
	id, err := GetUserID(r.Context())
	if err != nil {
		return ""
	}
	return id.String()
}

// RoleString returns the authenticated user's role, or "". Convenience
// wrapper around GetUserRole.
func RoleString(r *http.Request) string {
	return GetUserRole(r.Context())
}
