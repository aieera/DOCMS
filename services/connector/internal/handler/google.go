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
	"errors"
	"net/http"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/connector/internal/service"
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

// sessionToken pulls the caller's SeDoc session token from the request so
// the ingest path can act AS the user against the document service's
// SessionOrAPIKey-gated REST surface. Prefers the dms_session cookie
// (browser); falls back to a Bearer header (API clients).
func sessionToken(r *http.Request) string {
	if ck, err := r.Cookie("dms_session"); err == nil && ck.Value != "" {
		return ck.Value
	}
	const p = "Bearer "
	if h := r.Header.Get("Authorization"); len(h) > len(p) && h[:len(p)] == p {
		return h[len(p):]
	}
	return ""
}

type importGoogleDriveBody struct {
	// DriveFolderID is the Google Drive folder to import from. Empty =
	// "root" (My Drive top level).
	DriveFolderID string `json:"drive_folder_id"`
	// WorkspaceID + FolderID are the SeDoc destination.
	WorkspaceID string `json:"workspace_id"`
	FolderID    string `json:"folder_id"`
}

// importGoogleDrive imports the files in a Drive folder into a SeDoc
// workspace folder. Synchronous: the response carries the per-file
// outcome. A folder larger than the per-call cap returns truncated=true
// and the admin re-runs to continue.
func (h *Handler) importGoogleDrive(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "tenant and user required")
		return
	}
	var b importGoogleDriveBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if b.WorkspaceID == "" || b.FolderID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id and folder_id are required")
		return
	}
	res, err := h.svc.ImportDriveFolder(r.Context(), tenantID, userID, sessionToken(r), b.DriveFolderID, b.WorkspaceID, b.FolderID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrNoTenantTokens):
			writeError(w, http.StatusConflict, "google drive not connected — click Connect first")
		case errors.Is(err, service.ErrIngestUnavailable):
			writeError(w, http.StatusServiceUnavailable, "ingest path unavailable; storage may be down")
		default:
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, res)
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
