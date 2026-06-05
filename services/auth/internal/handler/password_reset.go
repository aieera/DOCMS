package handler

import (
	"errors"
	"net/http"

	"github.com/aieera/sedoc/services/auth/internal/service"
)

type forgotPasswordRequest struct {
	Email      string `json:"email"`
	TenantSlug string `json:"tenant_slug"`
}

// resetGenericMessage is returned for EVERY forgot-password call, registered or
// not — the enumeration-oracle defence (audit-2026-05 Track 2).
const resetGenericMessage = "If an account is registered with this email, a reset link has been sent."

// ForgotPassword handles POST /api/v1/auth/forgot-password. Always 200 with the
// same body regardless of whether the account exists; the service enforces a
// constant-time floor + per-email throttle.
func (h *Handler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req forgotPasswordRequest
	if err := h.readJSON(r, &req); err != nil {
		// A malformed body must not be a signal either — same generic 200.
		h.writeJSON(w, http.StatusOK, map[string]string{"message": resetGenericMessage})
		return
	}
	ip, _ := clientMeta(r)
	h.svc.RequestPasswordReset(r.Context(), req.Email, req.TenantSlug, ip)
	h.writeJSON(w, http.StatusOK, map[string]string{"message": resetGenericMessage})
}

type resetPasswordRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

// ResetPassword handles POST /api/v1/auth/reset-password. 204 on success;
// 410 for a used/expired token; 400 for a weak password; 401 for an
// unknown/malformed token. On success every session for the user is revoked.
func (h *Handler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var req resetPasswordRequest
	if err := h.readJSON(r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}
	if err := h.svc.ResetPassword(r.Context(), req.Token, req.NewPassword); err != nil {
		if errors.Is(err, service.ErrResetTokenGone) {
			h.writeJSON(w, http.StatusGone, map[string]string{"error": "this reset link is no longer valid"})
			return
		}
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
