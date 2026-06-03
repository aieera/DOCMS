// Microsoft 365 connector HTTP surface — ADR 0111.
//
// Endpoints (all gated by auth middleware upstream; tenant + user
// arrive via X-Auth-Tenant-ID and X-User-ID headers injected by the
// frontend's axios interceptor):
//
//   PUT  /api/v1/connectors/m365/config           save client_id + secret + entra tenant
//   GET  /api/v1/connectors/m365/auth-url         start OAuth (returns the authorize URL)
//   POST /api/v1/connectors/m365/disconnect       clear tokens, keep config
//   GET  /api/v1/connectors/m365/sites            list SharePoint sites
//   GET  /api/v1/connectors/m365/drives/{drive}/items
//                                                 list drive items (optional ?folder_id=)
//
// The shared OAuth callback lives in google.go's
// connectorOAuthCallback — it dispatches on the provider name embedded
// in the HMAC-signed state.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/connector/internal/service"
)

type putM365ConfigBody struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	// EntraTenant is the directory GUID for single-tenant Entra
	// apps. Empty / "common" routes through the multi-tenant
	// endpoint, which is the default for SaaS deployments.
	EntraTenant string `json:"entra_tenant,omitempty"`
}

func (h *Handler) putM365Config(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant required")
		return
	}
	var b putM365ConfigBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := h.svc.SaveM365Config(r.Context(), tenantID, userID, b.ClientID, b.ClientSecret, b.EntraTenant); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) m365AuthURL(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant required")
		return
	}
	url, err := h.svc.StartM365OAuth(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Field name mirrors Google's so the FE's connector API client
	// can be parameterised by provider name in a future refactor.
	writeJSON(w, http.StatusOK, map[string]string{"auth_url": url})
}

func (h *Handler) disconnectM365(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant required")
		return
	}
	if err := h.svc.DisconnectM365(r.Context(), tenantID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listM365Sites — GET /api/v1/connectors/m365/sites[?acting_as=<user>]
func (h *Handler) listM365Sites(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant required")
		return
	}
	sites, err := h.svc.ListM365Sites(r.Context(), tenantID, r.URL.Query().Get("acting_as"))
	if err != nil {
		statusForM365Err(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sites": sites})
}

// listM365DriveItems — GET /api/v1/connectors/m365/drives/{drive_id}/items
//                       [?folder_id=<id>][&acting_as=<user>]
func (h *Handler) listM365DriveItems(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant required")
		return
	}
	driveID := r.PathValue("drive_id")
	if driveID == "" {
		writeError(w, http.StatusBadRequest, "drive_id required")
		return
	}
	q := r.URL.Query()
	items, err := h.svc.ListM365DriveItems(r.Context(), tenantID, driveID, q.Get("folder_id"), q.Get("acting_as"))
	if err != nil {
		statusForM365Err(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// statusForM365Err maps service-layer errors to HTTP. The only one
// that gets a non-500 treatment is ErrNoTenantTokens — the FE uses
// 409 as the "needs OAuth handshake" signal so it can render the
// "Connect" button rather than a generic failure toast.
func statusForM365Err(w http.ResponseWriter, err error) {
	if errors.Is(err, service.ErrNoTenantTokens) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}
