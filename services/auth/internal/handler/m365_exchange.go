// Microsoft 365 → SeDoc session exchange — HTTP surface (ADR 0112).
//
//   POST /api/v1/auth/m365/exchange
//     Body: { "ms_access_token": "...", "tenant_id": "..." (optional) }
//     200:  { "vdms_session_token": "...", "user_id": "...", "tenant_id": "..." }
//     401:  Entra token expired / revoked.
//     404:  no SeDoc user with this email.
//     409:  email exists in N tenants — body lists candidates so
//           the add-in can prompt the user.
//
// The endpoint is intentionally UN-authenticated at the SeDoc
// layer — it's the entry point that ESTABLISHES auth. The proof
// of identity is the Entra token in the body; we validate it via
// Graph before issuing anything.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/aieera/sedoc/services/auth/internal/service"
)

type m365ExchangeReq struct {
	MSAccessToken string `json:"ms_access_token"`
	TenantID      string `json:"tenant_id,omitempty"`
}

type m365ExchangeResp struct {
	SessionToken string `json:"vdms_session_token"`
	UserID       string `json:"user_id"`
	TenantID     string `json:"tenant_id"`
	Email        string `json:"email"`
}

type m365MultiTenantResp struct {
	Error      string                          `json:"error"`
	Candidates []service.M365TenantCandidate   `json:"candidates"`
}

// ExchangeM365 handles POST /api/v1/auth/m365/exchange.
func (h *Handler) ExchangeM365(w http.ResponseWriter, r *http.Request) {
	var body m365ExchangeReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if body.MSAccessToken == "" {
		h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ms_access_token required"})
		return
	}
	ip := r.Header.Get("X-Forwarded-For")
	if ip == "" {
		ip = r.RemoteAddr
	}
	ua := r.UserAgent()
	res, err := h.svc.ExchangeM365Token(r.Context(), body.MSAccessToken, ip, ua, body.TenantID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrM365NoVDMSUser):
			h.writeJSON(w, http.StatusNotFound, map[string]string{
				"error": "no SeDoc user with this email; ask your SeDoc admin to invite you",
			})
			return
		case errors.Is(err, service.ErrM365TokenInvalid):
			// 401 per the documented contract — the add-in clears its
			// cache and re-runs the Office SSO exchange.
			h.writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "invalid or expired Entra access token",
			})
			return
		case errors.Is(err, service.ErrM365NotConfigured):
			h.writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": "M365 exchange is not configured on this deployment",
			})
			return
		}
		var multi *service.ErrM365MultipleTenants
		if errors.As(err, &multi) {
			h.writeJSON(w, http.StatusConflict, m365MultiTenantResp{
				Error:      "email belongs to multiple SeDoc tenants; re-call with tenant_id",
				Candidates: multi.Candidates,
			})
			return
		}
		// Anything else: a Graph 401, a Postgres hiccup, etc.
		// Don't leak the upstream error verbatim — the add-in
		// doesn't care, and a 500 keeps us safe from accidentally
		// echoing a sensitive substring.
		h.log.Error().Err(err).Msg("m365 exchange failed")
		h.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "exchange failed"})
		return
	}
	h.writeJSON(w, http.StatusOK, m365ExchangeResp{
		SessionToken: res.SessionToken,
		UserID:       res.UserID,
		TenantID:     res.TenantID,
		Email:        res.Email,
	})
}
