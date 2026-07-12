// Email-ingestion routes (ADR 0087).
//
// Mounts under /api/v1/admin/email-configs/* — all behind the
// gateway-signature + admin-role middleware.
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/connector/internal/email"
)

// EmailHandler wires ADR-0087 admin REST onto the connector service.
type EmailHandler struct {
	svc *email.Service
}

// NewEmailHandler constructs the handler.
func NewEmailHandler(svc *email.Service) *EmailHandler {
	return &EmailHandler{svc: svc}
}

// Register mounts routes on the supplied mux.
func (h *EmailHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/email-configs", h.list)
	mux.HandleFunc("POST /api/v1/admin/email-configs", h.create)
	mux.HandleFunc("PATCH /api/v1/admin/email-configs/{id}", h.patch)
	mux.HandleFunc("DELETE /api/v1/admin/email-configs/{id}", h.delete)
	mux.HandleFunc("POST /api/v1/admin/email-configs/{id}/run", h.runNow)
	mux.HandleFunc("GET /api/v1/admin/email-configs/{id}/stats", h.stats)
}

func (h *EmailHandler) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	out, err := h.svc.ListConfigs(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *EmailHandler) create(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	actorID := auth.UserIDString(r)
	var body email.CreateConfigInput
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	cfg, err := h.svc.CreateConfig(r.Context(), tenantID, actorID, body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, cfg)
}

func (h *EmailHandler) patch(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	id := r.PathValue("id")
	var body email.PatchConfigInput
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	cfg, err := h.svc.PatchConfig(r.Context(), tenantID, id, body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (h *EmailHandler) delete(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	id := r.PathValue("id")
	if err := h.svc.DeleteConfig(r.Context(), tenantID, id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *EmailHandler) runNow(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	id := r.PathValue("id")
	n, err := h.svc.RunOnce(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ingested": n})
}

func (h *EmailHandler) stats(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	id := r.PathValue("id")
	stats, err := h.svc.GetStats(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if stats == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}
