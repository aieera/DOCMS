// Package auth holds the authenticated-caller data model and the context
// helpers used to attach or read it. Middleware populates these values; any
// service handler or repository can read them without knowing how they were
// set.
package auth

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/vaultdms/vaultdms/pkg/logger"
)

// ErrMissing is returned by getters when the requested field is not present
// in the context.
var ErrMissing = errors.New("auth: value missing from context")

// UserInfo is the authenticated caller. It is the single source of truth used
// by services — they should never pass tenant / user data out-of-band.
type UserInfo struct {
	ID       uuid.UUID
	TenantID uuid.UUID
	Email    string
	Role     string
	Groups   []uuid.UUID
}

type userInfoKey struct{}

// WithUser stores the UserInfo and its tenant/user IDs on the context.
func WithUser(ctx context.Context, u UserInfo) context.Context {
	ctx = context.WithValue(ctx, userInfoKey{}, u)
	ctx = context.WithValue(ctx, logger.TenantIDKey, u.TenantID.String())
	ctx = context.WithValue(ctx, logger.UserIDKey, u.ID.String())
	return ctx
}

// User extracts UserInfo from the context. Returns ErrMissing if absent.
func User(ctx context.Context) (UserInfo, error) {
	u, ok := ctx.Value(userInfoKey{}).(UserInfo)
	if !ok {
		return UserInfo{}, ErrMissing
	}
	return u, nil
}

// SetTenantID stores the tenant ID (stringified UUID) on the context.
// Middleware uses this to populate the value extracted from a request header
// before a full UserInfo has been resolved.
func SetTenantID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, logger.TenantIDKey, id.String())
}

// GetTenantID returns the tenant UUID from the context.
func GetTenantID(ctx context.Context) (uuid.UUID, error) {
	s, ok := ctx.Value(logger.TenantIDKey).(string)
	if !ok || s == "" {
		return uuid.Nil, ErrMissing
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// SetCorrelationID stores a correlation ID on the context.
func SetCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, logger.CorrelationIDKey, id)
}

// GetCorrelationID returns the correlation ID from the context, or empty.
func GetCorrelationID(ctx context.Context) string {
	if s, ok := ctx.Value(logger.CorrelationIDKey).(string); ok {
		return s
	}
	return ""
}

// GetUserID returns the authenticated user's UUID.
func GetUserID(ctx context.Context) (uuid.UUID, error) {
	u, err := User(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	return u.ID, nil
}

// GetUserRole returns the authenticated user's role, or empty.
func GetUserRole(ctx context.Context) string {
	u, err := User(ctx)
	if err != nil {
		return ""
	}
	return u.Role
}

// GetUserGroups returns the authenticated user's group UUIDs.
func GetUserGroups(ctx context.Context) []uuid.UUID {
	u, err := User(ctx)
	if err != nil {
		return nil
	}
	return u.Groups
}

// GetUserName returns a human-readable identifier for the caller, used
// by audit/notification surfaces that don't want a raw UUID. Returns
// the user's email when set, empty otherwise (background goroutines
// without a request context, callers identified only by API key, etc).
// Audit consumer surfaces this as audit_events.actor_name.
func GetUserName(ctx context.Context) string {
	u, err := User(ctx)
	if err != nil {
		return ""
	}
	return u.Email
}

// clientIPKey identifies the request's client IP address on a context.
// Populated by HTTP middleware reading X-Forwarded-For / X-Real-IP /
// RemoteAddr in that order. Empty for ctx without a request (background
// jobs, cron, NATS consumers).
type clientIPKey struct{}

// WithClientIP stamps the client IP onto ctx so downstream callers can
// stash it on outbox rows / audit events without each layer having to
// re-parse the request.
func WithClientIP(ctx context.Context, ip string) context.Context {
	if ip == "" {
		return ctx
	}
	return context.WithValue(ctx, clientIPKey{}, ip)
}

// GetClientIP returns the client IP stamped by WithClientIP, or empty
// if the ctx didn't originate from an HTTP request.
func GetClientIP(ctx context.Context) string {
	if s, ok := ctx.Value(clientIPKey{}).(string); ok {
		return s
	}
	return ""
}

// scopesKey identifies the API-key scopes list on a request context.
// Only set when authentication came through an API key (Bearer); empty
// for session-cookie auth where scope is implicit in role.
type scopesKey struct{}

// WithScopes attaches an API key's scopes to ctx. Called by the
// APIKeyAuth middleware after a successful key lookup.
func WithScopes(ctx context.Context, scopes []string) context.Context {
	return context.WithValue(ctx, scopesKey{}, scopes)
}

// GetScopes returns the API-key scopes on ctx, or nil if none.
// MCP tool dispatch reads this to enforce per-tool scope (mcp:read,
// mcp:write) on top of the route-level scope already checked by
// APIKeyAuth.
func GetScopes(ctx context.Context) []string {
	v, ok := ctx.Value(scopesKey{}).([]string)
	if !ok {
		return nil
	}
	return v
}
