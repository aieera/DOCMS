package handler

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/auth/internal/service"
)

// ===========================================================================
// Registration
// ===========================================================================

type registerRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
	TenantSlug  string `json:"tenant_slug"`
}
type registerResponse struct {
	UserID      uuid.UUID `json:"user_id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	TenantID    uuid.UUID `json:"tenant_id"`
}

// Register handles POST /api/v1/auth/register.
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := h.readJSON(r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}
	user, err := h.svc.Register(r.Context(), service.RegisterInput{
		Email:       req.Email,
		Password:    req.Password,
		DisplayName: req.DisplayName,
		TenantSlug:  req.TenantSlug,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusCreated, registerResponse{
		UserID:      user.ID,
		Email:       user.Email,
		DisplayName: user.DisplayName,
		TenantID:    user.TenantID,
	})
}

// ===========================================================================
// Accept invite (public — token IS the auth)
// ===========================================================================

type acceptInviteRequest struct {
	TenantSlug string `json:"tenant_slug"`
	Token      string `json:"token"`
	Password   string `json:"password"`
}

type acceptInviteResponse struct {
	UserID      uuid.UUID `json:"user_id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	TenantID    uuid.UUID `json:"tenant_id"`
	TenantSlug  string    `json:"tenant_slug"`
}

// AcceptInvite handles POST /api/v1/auth/accept-invite. Public route —
// the plaintext token is the only secret required, and the rate limiter
// in the router caps brute-force attempts.
func (h *Handler) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	var req acceptInviteRequest
	if err := h.readJSON(r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}
	user, err := h.svc.AcceptInvite(r.Context(), service.AcceptInviteInput{
		TenantSlug: req.TenantSlug,
		Token:      req.Token,
		Password:   req.Password,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, acceptInviteResponse{
		UserID:      user.ID,
		Email:       user.Email,
		DisplayName: user.DisplayName,
		TenantID:    user.TenantID,
		TenantSlug:  req.TenantSlug,
	})
}

// ===========================================================================
// Login + MFA verify
// ===========================================================================

type loginRequest struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	TenantSlug string `json:"tenant_slug"`
}
type loginResponse struct {
	SessionToken    string              `json:"session_token,omitempty"`
	ExpiresAt       *time.Time          `json:"expires_at,omitempty"`
	User            any                 `json:"user,omitempty"`
	MFARequired     bool                `json:"mfa_required,omitempty"`
	MFASessionToken string              `json:"mfa_session_token,omitempty"`
}

// Login handles POST /api/v1/auth/login.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := h.readJSON(r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}
	ip, ua := clientMeta(r)
	res, err := h.svc.Login(r.Context(), service.LoginInput{
		Email:      req.Email,
		Password:   req.Password,
		TenantSlug: req.TenantSlug,
		IPAddress:  ip,
		UserAgent:  ua,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if res.MFARequired {
		h.writeJSON(w, http.StatusOK, loginResponse{
			MFARequired:     true,
			MFASessionToken: res.MFASessionToken,
		})
		return
	}
	h.setSessionCookie(w, res.Session.Token, res.Session.Session.ExpiresAt)
	h.setCSRFCookie(w, res.Session.Session.ExpiresAt)
	h.writeJSON(w, http.StatusOK, loginResponse{
		SessionToken: res.Session.Token,
		ExpiresAt:    &res.Session.Session.ExpiresAt,
		User:         res.Session.UserView,
	})
}

type mfaVerifyRequest struct {
	MFASessionToken string `json:"mfa_session_token"`
	TOTPCode        string `json:"totp_code"`
}

// MFAVerify handles POST /api/v1/auth/mfa/verify.
func (h *Handler) MFAVerify(w http.ResponseWriter, r *http.Request) {
	var req mfaVerifyRequest
	if err := h.readJSON(r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}
	ip, ua := clientMeta(r)
	sess, err := h.svc.VerifyMFA(r.Context(), req.MFASessionToken, req.TOTPCode, ip, ua)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.setSessionCookie(w, sess.Token, sess.Session.ExpiresAt)
	h.setCSRFCookie(w, sess.Session.ExpiresAt)
	h.writeJSON(w, http.StatusOK, loginResponse{
		SessionToken: sess.Token,
		ExpiresAt:    &sess.Session.ExpiresAt,
		User:         sess.UserView,
	})
}

type mfaRecoveryRequest struct {
	MFASessionToken string `json:"mfa_session_token"`
	RecoveryCode    string `json:"recovery_code"`
}

// MFARecovery handles POST /api/v1/auth/mfa/recovery.
func (h *Handler) MFARecovery(w http.ResponseWriter, r *http.Request) {
	var req mfaRecoveryRequest
	if err := h.readJSON(r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}
	ip, ua := clientMeta(r)
	sess, err := h.svc.VerifyRecoveryCode(r.Context(), req.MFASessionToken, req.RecoveryCode, ip, ua)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.setSessionCookie(w, sess.Token, sess.Session.ExpiresAt)
	h.setCSRFCookie(w, sess.Session.ExpiresAt)
	h.writeJSON(w, http.StatusOK, loginResponse{
		SessionToken: sess.Token,
		ExpiresAt:    &sess.Session.ExpiresAt,
		User:         sess.UserView,
	})
}

// ===========================================================================
// MFA setup / confirm / disable (authenticated)
// ===========================================================================

// MFASetup handles POST /api/v1/auth/mfa/setup.
func (h *Handler) MFASetup(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	// Email is already on ctx from AuthMiddleware — no extra round-trip.
	u, err := authUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out, err := h.svc.SetupMFA(r.Context(), tenantID, userID, u.Email)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"secret":         out.Secret,
		"qr_code_uri":    out.QRCodeURI,
		"recovery_codes": out.RecoveryCodes,
		"warning":        "Recovery codes are shown only once. Save them now.",
	})
}

type codeRequest struct {
	TOTPCode     string `json:"totp_code"`
	RecoveryCode string `json:"recovery_code"`
}

// MFAConfirm handles POST /api/v1/auth/mfa/confirm.
func (h *Handler) MFAConfirm(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	var req codeRequest
	if err := h.readJSON(r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}
	if err := h.svc.ConfirmMFA(r.Context(), tenantID, userID, req.TOTPCode); err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]bool{"mfa_enabled": true})
}

// MFADisable handles POST /api/v1/auth/mfa/disable.
func (h *Handler) MFADisable(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	var req codeRequest
	if err := h.readJSON(r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}
	if err := h.svc.DisableMFA(r.Context(), tenantID, userID, req.TOTPCode, req.RecoveryCode); err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]bool{"mfa_enabled": false})
}

// ===========================================================================
// Sessions
// ===========================================================================

// Me handles GET /api/v1/auth/me. Returns the authenticated user's
// PublicView so the frontend can rehydrate session state on hard reload.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	u, err := h.svc.GetUser(r.Context(), tenantID, userID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, u.ToPublic())
}

// Logout handles POST /api/v1/auth/logout.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	token, _ := h.extractBearer(r)
	_ = h.svc.Logout(r.Context(), token)
	h.clearSessionCookie(w)
	h.clearCSRFCookie(w)
	h.writeJSON(w, http.StatusNoContent, nil)
}

// ListSessions handles GET /api/v1/auth/sessions.
func (h *Handler) ListSessions(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	token, _ := h.extractBearer(r)
	currentHash := ""
	if token != "" {
		currentHash = sha256HexForHandler(token)
	}
	sessions, err := h.svc.ListUserSessions(r.Context(), tenantID, userID, currentHash)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

// RevokeAllSessions handles POST /api/v1/auth/sessions/revoke-all.
func (h *Handler) RevokeAllSessions(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	token, _ := h.extractBearer(r)
	currentHash := ""
	if token != "" {
		currentHash = sha256HexForHandler(token)
	}
	n, err := h.svc.RevokeAllOtherSessions(r.Context(), tenantID, userID, currentHash)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"revoked": n})
}

// RevokeSession handles DELETE /api/v1/auth/sessions/{session_id}.
func (h *Handler) RevokeSession(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "session_id"))
	if err != nil {
		h.writeError(w, r, vdmserr.Validation("session_id", "not a uuid"))
		return
	}
	if err := h.svc.RevokeSession(r.Context(), tenantID, userID, id); err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusNoContent, nil)
}

// ===========================================================================
// API keys
// ===========================================================================

type issueKeyRequest struct {
	Name          string   `json:"name"`
	Scopes        []string `json:"scopes"`
	ExpiresInDays int      `json:"expires_in_days"`
}

// IssueAPIKey handles POST /api/v1/auth/api-keys. Admin-only gate is
// enforced in the router middleware chain.
func (h *Handler) IssueAPIKey(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	var req issueKeyRequest
	if err := h.readJSON(r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}
	out, err := h.svc.IssueAPIKey(r.Context(), service.IssueAPIKeyInput{
		TenantID:      tenantID,
		UserID:        userID,
		Name:          req.Name,
		Scopes:        req.Scopes,
		ExpiresInDays: req.ExpiresInDays,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusCreated, out)
}

// ListAPIKeys handles GET /api/v1/auth/api-keys.
func (h *Handler) ListAPIKeys(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	keys, err := h.svc.ListAPIKeys(r.Context(), tenantID, userID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	// Strip hash just in case the repo forgot.
	type safeKey struct {
		ID         uuid.UUID  `json:"key_id"`
		KeyPrefix  string     `json:"key_prefix"`
		Name       string     `json:"name"`
		Scopes     []string   `json:"scopes"`
		CreatedAt  time.Time  `json:"created_at"`
		LastUsedAt *time.Time `json:"last_used_at,omitempty"`
		ExpiresAt  *time.Time `json:"expires_at,omitempty"`
		RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	}
	out := make([]safeKey, 0, len(keys))
	for _, k := range keys {
		out = append(out, safeKey{
			ID: k.ID, KeyPrefix: k.KeyPrefix, Name: k.Name, Scopes: k.Scopes,
			CreatedAt: k.CreatedAt, LastUsedAt: k.LastUsedAt,
			ExpiresAt: k.ExpiresAt, RevokedAt: k.RevokedAt,
		})
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"api_keys": out})
}

// RevokeAPIKey handles DELETE /api/v1/auth/api-keys/{key_id}.
func (h *Handler) RevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "key_id"))
	if err != nil {
		h.writeError(w, r, vdmserr.Validation("key_id", "not a uuid"))
		return
	}
	if err := h.svc.RevokeAPIKey(r.Context(), tenantID, userID, id); err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusNoContent, nil)
}

// ---- helpers -------------------------------------------------------------

// sha256HexForHandler wraps the service's exported helper to keep this
// package free of crypto imports.
func sha256HexForHandler(s string) string {
	return service.Sha256HexForHandler(s)
}

// authUser is a thin alias for pkg/auth.User to shorten call sites.
func authUser(r *http.Request) (authUserInfo, error) {
	u, err := authUserFromCtx(r)
	if err != nil {
		return authUserInfo{}, vdmserr.ErrUnauthorized
	}
	return u, nil
}
