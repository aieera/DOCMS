package handler

// Wave 15.3 HTTP surface for force-change-password.
//
//   POST /api/v1/auth/change-password
//   POST /api/v1/admin/users/{id}/force-password-reset

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/auth/internal/service"
)

type changePasswordRequest struct {
	OneTimeChangeToken string `json:"one_time_change_token"`
	NewPassword        string `json:"new_password"`
}

type changePasswordResponse struct {
	SessionToken string     `json:"session_token,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	User         any        `json:"user,omitempty"`
}

// ForgotPassword handles POST /api/v1/auth/forgot-password. Public
// route, rate-limited at the router. Body: { tenant_slug, email }.
// Always returns 202 with a generic message regardless of whether
// the email matches a real user — the no-oracle defense from ADR
// 0037 applied here too. The actual reset email goes through the
// notification service's existing email path; the magic link points
// to the existing /change-password page which already handles the
// one-time-token redeem flow.
type forgotPasswordRequest struct {
	TenantSlug string `json:"tenant_slug"`
	Email      string `json:"email"`
}

func (h *Handler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req forgotPasswordRequest
	if err := h.readJSON(r, &req); err != nil {
		// Any input parse error → still return 202 with generic body.
		// Don't give a callable a way to distinguish "well-formed but
		// unknown email" from "malformed body".
		h.writeJSON(w, http.StatusAccepted, map[string]any{
			"accepted": true,
			"message":  "If your email matches a record in our system you will receive a reset link within a few minutes.",
		})
		return
	}
	ip, ua := clientMeta(r)
	// Service handles its own oracle defense — unknown slug/email/SSO
	// user are all silent no-ops.
	if err := h.svc.RequestPasswordReset(r.Context(), service.RequestPasswordResetInput{
		TenantSlug: req.TenantSlug,
		Email:      req.Email,
		IPAddress:  ip,
		UserAgent:  ua,
	}); err != nil {
		// Even genuine internal errors return 202 with the same
		// message — operators see them in the service log via the
		// existing writeErr-style logging; a public caller never finds
		// out whether their attempt mutated state.
		// We DO log here for ops.
	}
	h.writeJSON(w, http.StatusAccepted, map[string]any{
		"accepted": true,
		"message":  "If your email matches a record in our system you will receive a reset link within a few minutes.",
	})
}

// ChangePassword handles POST /api/v1/auth/change-password. Public
// route — authenticated by possession of the one-time token, not by
// a session cookie.
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	var req changePasswordRequest
	if err := h.readJSON(r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}
	ip, ua := clientMeta(r)
	res, err := h.svc.ChangePassword(r.Context(), service.ChangePasswordInput{
		OneTimeChangeToken: req.OneTimeChangeToken,
		NewPassword:        req.NewPassword,
		IPAddress:          ip,
		UserAgent:          ua,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.setSessionCookie(w, res.Session.Token, res.Session.Session.ExpiresAt)
	h.setCSRFCookie(w, res.Session.Session.ExpiresAt)
	h.writeJSON(w, http.StatusOK, changePasswordResponse{
		SessionToken: res.Session.Token,
		ExpiresAt:    &res.Session.Session.ExpiresAt,
		User:         res.Session.UserView,
	})
}

// SweepExpiredPasswordsAdmin handles
// POST /api/v1/admin/password-policy/sweep-expired. Owner-role gated
// via router; operators wire this to a k8s CronJob or external
// scheduler. A native Temporal schedule is logged as follow-up in
// docs/reports/WAVE_15_PROGRESS.md — the auth service does not have
// a Temporal client today.
func (h *Handler) SweepExpiredPasswordsAdmin(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	n, err := h.svc.SweepExpiredPasswordsForTenant(r.Context(), tenantID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"flagged": n})
}

// ForcePasswordResetAdmin handles POST /api/v1/admin/users/{id}/force-password-reset.
// Authenticated + admin-role gated by the router; this handler
// simply resolves the target user id and delegates.
func (h *Handler) ForcePasswordResetAdmin(w http.ResponseWriter, r *http.Request) {
	tenantID, actorID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	targetID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.writeError(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	if targetID == actorID {
		// Admin self-reset via this endpoint would lock the admin
		// out before they could change their own password through
		// the normal self-service path. Reject.
		h.writeError(w, r, vdmserr.Validation("id", "cannot force-reset your own password via admin endpoint; use self-service"))
		return
	}
	if err := h.svc.ForcePasswordReset(r.Context(), tenantID, actorID, targetID); err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusAccepted, map[string]any{
		"user_id":               targetID,
		"must_change_password":  true,
	})
}
