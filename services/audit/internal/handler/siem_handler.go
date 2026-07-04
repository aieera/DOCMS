// SIEM sink admin API (§15). Per-tenant CRUD for SIEM forwarding sinks, a
// send-test-event action, and delivery-health (folded into the sink rows).
// Admin-gated at the gateway (/api/v1/admin/siem).
//
//	GET/POST         /api/v1/admin/siem/sinks
//	GET/PUT/DELETE   /api/v1/admin/siem/sinks/{id}
//	POST             /api/v1/admin/siem/sinks/{id}/test
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/audit/internal/siem"
)

type SIEMHandler struct {
	svc *siem.Service
	log zerolog.Logger
}

func NewSIEMHandler(svc *siem.Service, log zerolog.Logger) *SIEMHandler {
	return &SIEMHandler{svc: svc, log: log}
}

func (h *SIEMHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/siem/sinks", h.list)
	mux.HandleFunc("POST /api/v1/admin/siem/sinks", h.create)
	mux.HandleFunc("GET /api/v1/admin/siem/sinks/{id}", h.get)
	mux.HandleFunc("PUT /api/v1/admin/siem/sinks/{id}", h.update)
	mux.HandleFunc("DELETE /api/v1/admin/siem/sinks/{id}", h.delete)
	mux.HandleFunc("POST /api/v1/admin/siem/sinks/{id}/test", h.test)
}

// mask never returns the stored token to the client.
func mask(s *siem.Sink) *siem.Sink {
	if s != nil {
		s.Token = ""
	}
	return s
}

func (h *SIEMHandler) tenant(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	t := auth.TenantIDString(r)
	if t == "" {
		writeError(w, http.StatusBadRequest, "unauthenticated: no tenant on session")
		return uuid.Nil, false
	}
	// Admin/owner only — the gateway's `auth: admin` class documents the
	// intent but role enforcement is backend-side (kong.yaml auth model).
	// Without this gate any tenant member could point the tenant's audit
	// stream at an attacker-controlled sink.
	switch auth.GetUserRole(r.Context()) {
	case "admin", "owner":
	default:
		writeError(w, http.StatusForbidden, "admin role required")
		return uuid.Nil, false
	}
	id, err := uuid.Parse(t)
	if err != nil {
		writeError(w, http.StatusBadRequest, "tenant not a uuid")
		return uuid.Nil, false
	}
	return id, true
}

func (h *SIEMHandler) list(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenant(w, r)
	if !ok {
		return
	}
	sinks, err := h.svc.List(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	for _, s := range sinks {
		mask(s)
	}
	writeJSON(w, http.StatusOK, map[string]any{"sinks": sinks})
}

func (h *SIEMHandler) create(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenant(w, r)
	if !ok {
		return
	}
	var in siem.SinkInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	out, err := h.svc.Create(r.Context(), tenantID, in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, mask(out))
}

func (h *SIEMHandler) get(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenant(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "id not a uuid")
		return
	}
	out, err := h.svc.Get(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, mask(out))
}

func (h *SIEMHandler) update(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenant(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "id not a uuid")
		return
	}
	var in siem.SinkInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	out, err := h.svc.Update(r.Context(), tenantID, id, in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, mask(out))
}

func (h *SIEMHandler) delete(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenant(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "id not a uuid")
		return
	}
	if err := h.svc.Delete(r.Context(), tenantID, id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

func (h *SIEMHandler) test(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenant(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "id not a uuid")
		return
	}
	if err := h.svc.SendTestEvent(r.Context(), tenantID, id); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
