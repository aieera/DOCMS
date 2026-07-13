// Watched-folder intake routes (ADR 0088).
//
// Mounts under /api/v1/admin/intake/folders/*.
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/connector/internal/intake"
)

// IntakeHandler wires ADR-0088 admin REST onto the connector service.
type IntakeHandler struct{ svc *intake.Service }

// NewIntakeHandler constructs the handler.
func NewIntakeHandler(svc *intake.Service) *IntakeHandler { return &IntakeHandler{svc: svc} }

// Register mounts routes on the supplied mux.
func (h *IntakeHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/intake/folders", h.list)
	mux.HandleFunc("POST /api/v1/admin/intake/folders", h.create)
	mux.HandleFunc("PATCH /api/v1/admin/intake/folders/{id}", h.patch)
	mux.HandleFunc("DELETE /api/v1/admin/intake/folders/{id}", h.delete)
	mux.HandleFunc("POST /api/v1/admin/intake/folders/{id}/scan-now", h.scanNow)
	mux.HandleFunc("GET /api/v1/admin/intake/folders/{id}/recent-files", h.recent)
}

func (h *IntakeHandler) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	out, err := h.svc.ListFolders(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *IntakeHandler) create(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	actorID := auth.UserIDString(r)
	var body intake.CreateInput
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	cfg, err := h.svc.CreateFolder(r.Context(), tenantID, actorID, body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, cfg)
}

func (h *IntakeHandler) patch(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	id := r.PathValue("id")
	var body intake.PatchInput
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	cfg, err := h.svc.PatchFolder(r.Context(), tenantID, id, body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (h *IntakeHandler) delete(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	id := r.PathValue("id")
	if err := h.svc.DeleteFolder(r.Context(), tenantID, id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *IntakeHandler) scanNow(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	id := r.PathValue("id")
	if err := h.svc.ScanNow(r.Context(), tenantID, id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *IntakeHandler) recent(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	id := r.PathValue("id")
	out, err := h.svc.RecentFiles(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}
