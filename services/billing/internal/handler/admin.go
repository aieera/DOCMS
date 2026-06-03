// User-facing admin settings endpoints: GET + PUT /api/v1/admin/settings.
// These wrap the internal GetFeatureFlags / UpdateFeatureFlags calls behind
// a session-authenticated role check (owner only — billing-affecting flags).
// The existing /internal/v1/tenants/{tenantId}/features endpoints remain
// in place for service-to-service calls authed by X-API-Key.
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/billing/internal/model"
)

// RegisterAdmin mounts the /api/v1/admin/settings routes on the supplied
// mux. Callers must wrap the mux with middleware.SessionAuth +
// middleware.RequireRole("owner") so auth.User(ctx) is populated and the
// role gate has already run.
func (h *Handler) RegisterAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/settings", h.getAdminSettings)
	mux.HandleFunc("PUT /api/v1/admin/settings", h.updateAdminSettings)
}

func (h *Handler) getAdminSettings(w http.ResponseWriter, r *http.Request) {
	u, err := auth.User(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	flags, err := h.svc.GetFeatureFlags(r.Context(), u.TenantID.String())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, flags)
}

func (h *Handler) updateAdminSettings(w http.ResponseWriter, r *http.Request) {
	u, err := auth.User(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var flags model.FeatureFlags
	if err := json.NewDecoder(r.Body).Decode(&flags); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := h.svc.UpdateFeatureFlags(r.Context(), u.TenantID.String(), &flags); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, flags)
}
