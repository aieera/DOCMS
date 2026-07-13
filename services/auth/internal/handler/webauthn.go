// ADR 0061 — WebAuthn / Passkey HTTP handlers.
//
// Six routes:
//
//	POST   /api/v1/auth/webauthn/registration/begin   (authenticated)
//	POST   /api/v1/auth/webauthn/registration/finish  (authenticated)
//	POST   /api/v1/auth/webauthn/login/begin          (public)
//	POST   /api/v1/auth/webauthn/login/finish         (public)
//	GET    /api/v1/auth/webauthn/credentials          (authenticated)
//	DELETE /api/v1/auth/webauthn/credentials/{id}     (authenticated)
//
// Wire format: binary fields (credential_id, attestation, assertion)
// move as base64url strings — that's what the browser produces from
// ArrayBuffers via the standard WebAuthn JSON shape, so the
// frontend can pass it through verbatim.
package handler

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aieera/sedoc/services/auth/internal/model"
	"github.com/aieera/sedoc/services/auth/internal/service"
)

// ---- Registration begin --------------------------------------------

type webauthnRegStartResponse struct {
	// Options is the protocol.CredentialCreation produced by the
	// lib — handed verbatim to navigator.credentials.create().
	Options      json.RawMessage `json:"options"`
	SessionToken string          `json:"session_token"`
}

// WebAuthnRegistrationStart handles POST /auth/webauthn/registration/begin.
func (h *Handler) WebAuthnRegistrationStart(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	options, token, err := h.svc.PasskeyRegistrationStart(r.Context(), tenantID, userID)
	if err != nil {
		h.writeWebAuthnError(w, r, err)
		return
	}
	rawOpts, err := json.Marshal(options)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, webauthnRegStartResponse{
		Options:      rawOpts,
		SessionToken: token,
	})
}

// ---- Registration finish -------------------------------------------

type webauthnRegFinishRequest struct {
	SessionToken        string          `json:"session_token"`
	FriendlyName        string          `json:"friendly_name"`
	AttestationResponse json.RawMessage `json:"attestation_response"`
}

type webauthnCredentialView struct {
	CredentialID   string     `json:"credential_id"`
	Name           string     `json:"name"`
	AAGUID         string     `json:"aaguid,omitempty"`
	Transports     []string   `json:"transports"`
	BackupEligible bool       `json:"backup_eligible"`
	BackupState    bool       `json:"backup_state"`
	CreatedAt      time.Time  `json:"created_at"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
}

func credentialToView(c *model.WebAuthnCredential) webauthnCredentialView {
	transports := c.Transports
	if transports == nil {
		transports = []string{}
	}
	return webauthnCredentialView{
		CredentialID:   base64.RawURLEncoding.EncodeToString(c.CredentialID),
		Name:           c.Name,
		AAGUID:         base64.RawURLEncoding.EncodeToString(c.AAGUID),
		Transports:     transports,
		BackupEligible: c.BackupEligible,
		BackupState:    c.BackupState,
		CreatedAt:      c.CreatedAt,
		LastUsedAt:     c.LastUsedAt,
	}
}

// WebAuthnRegistrationFinish handles POST /auth/webauthn/registration/finish.
func (h *Handler) WebAuthnRegistrationFinish(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	var req webauthnRegFinishRequest
	if err := h.readJSON(r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}
	cred, err := h.svc.PasskeyRegistrationFinish(r.Context(), tenantID,
		req.SessionToken, req.FriendlyName, []byte(req.AttestationResponse))
	if err != nil {
		h.writeWebAuthnError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusCreated, credentialToView(cred))
}

// ---- Login begin (public) ------------------------------------------

type webauthnLoginStartRequest struct {
	TenantSlug string `json:"tenant_slug"`
	Email      string `json:"email"`
}

type webauthnLoginStartResponse struct {
	Options      json.RawMessage `json:"options"`
	SessionToken string          `json:"session_token"`
}

// WebAuthnLoginStart handles POST /auth/webauthn/login/begin.
func (h *Handler) WebAuthnLoginStart(w http.ResponseWriter, r *http.Request) {
	var req webauthnLoginStartRequest
	if err := h.readJSON(r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}
	options, token, err := h.svc.PasskeyLoginStart(r.Context(), req.TenantSlug, req.Email)
	if err != nil {
		h.writeWebAuthnError(w, r, err)
		return
	}
	rawOpts, err := json.Marshal(options)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, webauthnLoginStartResponse{
		Options:      rawOpts,
		SessionToken: token,
	})
}

// ---- Login finish (public) -----------------------------------------

type webauthnLoginFinishRequest struct {
	SessionToken      string          `json:"session_token"`
	AssertionResponse json.RawMessage `json:"assertion_response"`
}

// WebAuthnLoginFinish handles POST /auth/webauthn/login/finish.
// Issues the standard session cookie + returns an auth/login-shaped
// body so the client doesn't have to round-trip /auth/me to render.
func (h *Handler) WebAuthnLoginFinish(w http.ResponseWriter, r *http.Request) {
	var req webauthnLoginFinishRequest
	if err := h.readJSON(r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}
	ip, ua := clientMeta(r)
	created, err := h.svc.PasskeyLoginFinish(r.Context(), req.SessionToken,
		[]byte(req.AssertionResponse), ip, ua)
	if err != nil {
		h.writeWebAuthnError(w, r, err)
		return
	}
	h.setSessionCookie(w, created.Token, created.Session.ExpiresAt)
	h.setCSRFCookie(w, created.Session.ExpiresAt)
	h.writeJSON(w, http.StatusOK, loginResponse{
		SessionToken: created.Token,
		ExpiresAt:    &created.Session.ExpiresAt,
		User:         created.UserView,
	})
}

// ---- List + Delete (authenticated) ---------------------------------

// WebAuthnList returns the user's passkeys for /settings/security.
func (h *Handler) WebAuthnList(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	creds, err := h.svc.ListPasskeys(r.Context(), tenantID, userID)
	if err != nil {
		h.writeWebAuthnError(w, r, err)
		return
	}
	out := make([]webauthnCredentialView, 0, len(creds))
	for _, c := range creds {
		out = append(out, credentialToView(c))
	}
	h.writeJSON(w, http.StatusOK, out)
}

// WebAuthnDelete removes one passkey.
func (h *Handler) WebAuthnDelete(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	idParam := chi.URLParam(r, "id")
	credID, err := base64.RawURLEncoding.DecodeString(idParam)
	if err != nil {
		http.Error(w, "invalid credential id", http.StatusBadRequest)
		return
	}
	if err := h.svc.DeletePasskey(r.Context(), tenantID, userID, credID); err != nil {
		h.writeWebAuthnError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- error mapping --------------------------------------------------

// writeWebAuthnError maps the service's typed errors to canonical
// HTTP codes:
//
//	ErrWebAuthnNotImplemented → 501
//	ErrInvalidSession         → 400 (restart the flow)
//	ErrNoPasskeysRegistered   → 404 (register one in Settings → Security)
//	anything else             → writeError (typically 500)
func (h *Handler) writeWebAuthnError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrWebAuthnNotImplemented):
		h.writeJSON(w, http.StatusNotImplemented, map[string]string{
			"error": "passkey support not configured on this deploy (set SEDOC_WEBAUTHN_RPID)",
		})
	case errors.Is(err, service.ErrInvalidSession):
		h.writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "session_token invalid or expired — restart the flow",
		})
	case errors.Is(err, service.ErrNoPasskeysRegistered):
		// First-time visitor clicking "Sign in with passkey" before
		// registering one. 404 with a hint pointing at the
		// registration path; frontend surfaces a friendlier toast.
		h.writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "no passkey registered for this account — sign in with your password and add one in Settings → Security",
		})
	default:
		h.writeError(w, r, err)
	}
}
