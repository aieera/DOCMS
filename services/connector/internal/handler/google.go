// Google Workspace connector HTTP surface — ADR 0089.
//
// Endpoints (all gated by auth middleware upstream; tenant + user
// arrive via X-Auth-Tenant-ID and X-User-ID headers injected by the
// frontend's axios interceptor):
//
//   PUT  /api/v1/connectors/google/config         save client_id + secret
//   POST /api/v1/connectors/google/disconnect     clear tokens, keep config
//
// The shared connector routes (auth-url, oauth/callback) live in
// handler.go and dispatch on the {provider} path param.
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/aieera/sedoc/pkg/auth"
)

type putGoogleConfigBody struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

func (h *Handler) putGoogleConfig(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant required")
		return
	}
	var b putGoogleConfigBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := h.svc.SaveGoogleConfig(r.Context(), tenantID, userID, b.ClientID, b.ClientSecret); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) disconnectGoogle(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant required")
		return
	}
	if err := h.svc.DisconnectGoogle(r.Context(), tenantID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// connectorOAuthCallback handles vendor-side redirects for ALL
// native connectors. The provider name rides inside the HMAC-signed
// state (same pattern as the eSign callback), so this one handler
// dispatches to the right service method.
//
// Layout of `state`:  <tenant_id>.<provider>.<hmac-hex>
// We split on '.' just enough to peek at the provider; the full
// HMAC verification happens inside the service method.
func (h *Handler) connectorOAuthCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		writeError(w, http.StatusBadRequest, "code and state required")
		return
	}
	// Peek the provider out of the state without trusting it — the
	// service method re-validates the HMAC before doing anything
	// stateful.
	provider := peekProviderFromState(state)

	var target string
	var err error
	switch provider {
	case "google":
		target, err = h.svc.HandleGoogleOAuthCallback(r.Context(), code, state)
	case "m365":
		target, err = h.svc.HandleM365OAuthCallback(r.Context(), code, state)
	default:
		writeError(w, http.StatusBadRequest, "unknown connector in state")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// peekProviderFromState extracts the middle segment of the connector
// state without trusting it. Returns "" when the state is malformed —
// the dispatcher then 400s, which is the right outcome for any state
// that doesn't roundtrip through our signConnectorState() helper.
func peekProviderFromState(state string) string {
	first := indexByte(state, '.')
	if first < 0 {
		return ""
	}
	rest := state[first+1:]
	second := indexByte(rest, '.')
	if second < 0 {
		return ""
	}
	return rest[:second]
}

// indexByte avoids importing "strings" for a one-byte search.
func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
