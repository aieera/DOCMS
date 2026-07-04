// Google Workspace → SeDoc session exchange — HTTP surface (ADR 0116).
//
//	POST /api/v1/auth/google/exchange
//	  Body: { "google_id_token": "...", "tenant_id": "..." (optional) }
//	  200:  { "vdms_session_token": "...", "user_id": "...", "tenant_id": "...", "email": "..." }
//	  401:  Google ID token expired / revoked / wrong audience.
//	  404:  no SeDoc user with this email.
//	  409:  email exists in N tenants — body lists candidates so the
//	        add-on can prompt the user.
//
// Like /m365/exchange, this endpoint is intentionally UN-authenticated
// at the SeDoc layer — it is the entry point that ESTABLISHES auth. The
// proof of identity is the Google OIDC ID token in the body, which we
// verify against Google's JWKS before issuing anything.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/aieera/sedoc/services/auth/internal/service"
)

type googleExchangeReq struct {
	GoogleIDToken string `json:"google_id_token"`
	TenantID      string `json:"tenant_id,omitempty"`
}

type googleExchangeResp struct {
	SessionToken string `json:"vdms_session_token"`
	UserID       string `json:"user_id"`
	TenantID     string `json:"tenant_id"`
	Email        string `json:"email"`
}

type googleMultiTenantResp struct {
	Error      string                    `json:"error"`
	Candidates []service.TenantCandidate `json:"candidates"`
}

// ExchangeGoogle handles POST /api/v1/auth/google/exchange.
func (h *Handler) ExchangeGoogle(w http.ResponseWriter, r *http.Request) {
	var body googleExchangeReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if body.GoogleIDToken == "" {
		h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "google_id_token required"})
		return
	}
	ip := r.Header.Get("X-Forwarded-For")
	if ip == "" {
		ip = r.RemoteAddr
	}
	ua := r.UserAgent()
	res, err := h.svc.ExchangeGoogleToken(r.Context(), body.GoogleIDToken, ip, ua, body.TenantID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrGoogleNoVDMSUser):
			h.writeJSON(w, http.StatusNotFound, map[string]string{
				"error": "no SeDoc user with this email; ask your SeDoc admin to invite you",
			})
			return
		case errors.Is(err, service.ErrGoogleTokenInvalid):
			// Routine path — Apps Script identity tokens expire hourly.
			// 401 tells the add-on to re-acquire and retry, not to treat
			// this as an outage. Body stays generic (no verifier detail).
			h.writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "invalid or expired Google identity token",
			})
			return
		case errors.Is(err, service.ErrGoogleNotConfigured):
			h.writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": "Google exchange is not configured on this deployment",
			})
			return
		}
		var multi *service.ErrGoogleMultipleTenants
		if errors.As(err, &multi) {
			h.writeJSON(w, http.StatusConflict, googleMultiTenantResp{
				Error:      "email belongs to multiple SeDoc tenants; re-call with tenant_id",
				Candidates: multi.Candidates,
			})
			return
		}
		// Anything else (a Google JWKS failure, a token rejection, a
		// Postgres hiccup): don't leak the upstream error verbatim.
		// A 500 keeps us safe from accidentally echoing a sensitive
		// substring; a bad/expired token is the common case here.
		h.log.Error().Err(err).Msg("google exchange failed")
		h.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "exchange failed"})
		return
	}
	h.writeJSON(w, http.StatusOK, googleExchangeResp{
		SessionToken: res.SessionToken,
		UserID:       res.UserID,
		TenantID:     res.TenantID,
		Email:        res.Email,
	})
}
