package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/aieera/sedoc/pkg/auth"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

// AuthMiddleware validates a session token OR API key and populates the
// request context with UserInfo. This is the single middleware every other
// SeDoc service imports from the auth package and chains in front of its
// own handlers.
//
// Wire order expected upstream:
//
//	correlation → recovery → requestlog → AuthMiddleware → tenant
//
// AuthMiddleware sets the tenant string on the ctx; the downstream `tenant`
// middleware picks it up and runs SET LOCAL app.current_tenant.
func (h *Handler) AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, isAPIKey := h.extractBearer(r)
		if token == "" {
			h.writeError(w, r, vdmserr.ErrUnauthorized)
			return
		}

		var (
			ctx      context.Context = r.Context()
			userInfo auth.UserInfo
		)

		if isAPIKey {
			key, err := h.svc.ValidateAPIKey(ctx, token, "")
			if err != nil {
				h.writeError(w, r, err)
				return
			}
			userInfo = auth.UserInfo{
				ID:       key.UserID,
				TenantID: key.TenantID,
				// Role is intentionally not escalated by API keys; scopes are
				// the authorization primitive for API-key callers.
				Role: "api_key",
			}
		} else {
			cached, err := h.svc.ValidateSession(ctx, token)
			if err != nil {
				h.writeError(w, r, err)
				return
			}
			userInfo = auth.UserInfo{
				ID:       cached.UserID,
				TenantID: cached.TenantID,
				Email:    cached.Email,
				Role:     string(cached.Role),
				Groups:   cached.Groups,
			}
		}

		ctx = auth.WithUser(ctx, userInfo)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRole returns a middleware that 403s when the authenticated user's
// role isn't in the allowed set. Must be chained AFTER AuthMiddleware.
func (h *Handler) RequireRole(roles ...string) func(http.Handler) http.Handler {
	allowed := map[string]struct{}{}
	for _, r := range roles {
		allowed[r] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role := auth.GetUserRole(r.Context())
			if _, ok := allowed[role]; !ok {
				h.writeError(w, r, vdmserr.ErrForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireScope returns a middleware that enforces an API-key scope. Used
// on endpoints that may be called by API keys; session-authenticated users
// bypass the scope check (they have full role-based permissions). The
// middleware inspects the Authorization header to detect the API-key path.
func (h *Handler) RequireScope(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Detect the API-key path using the SAME parser that authenticates
			// it (extractBearer, which TrimSpaces). The old prefix test used
			// TrimPrefix(..., "Bearer ") which disagreed on extra whitespace, so
			// an "Authorization: Bearer  vdms_…" (double space) key still
			// authenticated but SKIPPED this scope check.
			token, _ := h.extractBearer(r)
			if strings.HasPrefix(token, "vdms_") {
				// Re-validate with required scope; service returns Forbidden
				// if the key lacks it.
				if _, err := h.svc.ValidateAPIKey(r.Context(), token, scope); err != nil {
					h.writeError(w, r, err)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
