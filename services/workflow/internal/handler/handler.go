// Package handler exposes REST endpoints for the workflow service.
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/services/workflow/internal/model"
	"github.com/vaultdms/vaultdms/services/workflow/internal/service"

	"errors"
)

// Handler holds HTTP route handlers.
type Handler struct {
	svc *service.Service
	log zerolog.Logger
}

// New constructs a Handler.
func New(svc *service.Service, log zerolog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

// Register mounts routes.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/workflows/definitions", h.listDefinitions)
	mux.HandleFunc("POST /api/v1/workflows/definitions", h.createDefinition)
	mux.HandleFunc("GET /api/v1/workflows/definitions/{id}", h.getDefinition)
	mux.HandleFunc("PUT /api/v1/workflows/definitions/{id}", h.updateDefinition)
	mux.HandleFunc("DELETE /api/v1/workflows/definitions/{id}", h.deleteDefinition)
	mux.HandleFunc("POST /api/v1/workflows/definitions/{id}/visibility", h.setDefinitionVisibility)
	mux.HandleFunc("GET /api/v1/workflows/definitions/{id}/grants", h.listDefinitionGrants)
	mux.HandleFunc("POST /api/v1/workflows/definitions/{id}/grants", h.addDefinitionGrant)
	mux.HandleFunc("DELETE /api/v1/workflows/definitions/{id}/grants/{granteeType}/{granteeId}", h.removeDefinitionGrant)
	mux.HandleFunc("POST /api/v1/workflows/instances", h.startInstance)
	mux.HandleFunc("GET /api/v1/workflows/instances", h.listInstances)
	mux.HandleFunc("GET /api/v1/workflows/instances/{id}", h.getInstance)
	mux.HandleFunc("GET /api/v1/workflows/instances/{id}/timeline", h.getInstanceTimeline)
	mux.HandleFunc("POST /api/v1/workflows/instances/{id}/signal", h.signalStep)
	mux.HandleFunc("POST /api/v1/workflows/instances/{id}/cancel", h.cancelInstance)
	mux.HandleFunc("GET /api/v1/workflows/documents/{document_id}", h.getActiveInstanceByDocument)
	mux.HandleFunc("GET /api/v1/workflows/tasks/mine", h.listMyTasks)
	mux.HandleFunc("GET /api/v1/workflows/tasks", h.listTasks)
}

func (h *Handler) listDefinitions(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID required")
		return
	}
	defs, err := h.svc.ListDefinitions(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	writeJSON(w, http.StatusOK, defs)
}

type createDefBody struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Steps       []model.Step `json:"steps"`
	Visibility  string       `json:"visibility,omitempty"`
}

func (h *Handler) createDefinition(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	userID := r.Header.Get("X-User-ID")
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID and X-User-ID required")
		return
	}
	var body createDefBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		writeError(w, http.StatusBadRequest, "name required")
		return
	}
	def, err := h.svc.CreateDefinition(r.Context(), tenantID, body.Name, body.Description, userID, body.Visibility, body.Steps)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create failed")
		return
	}
	writeJSON(w, http.StatusCreated, def)
}

type startBody struct {
	DefinitionID string `json:"definition_id"`
	DocumentID   string `json:"document_id"`
}

func (h *Handler) startInstance(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	userID := r.Header.Get("X-User-ID")
	var body startBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	inst, err := h.svc.StartInstance(r.Context(), tenantID, body.DefinitionID, body.DocumentID, userID)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		h.log.Error().Err(err).Msg("start instance")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, inst)
}

func (h *Handler) getInstance(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	id := r.PathValue("id")
	inst, err := h.svc.GetInstance(r.Context(), tenantID, id)
	if err != nil || inst == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, inst)
}

func (h *Handler) getInstanceTimeline(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	id := r.PathValue("id")
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID required")
		return
	}
	tasks, err := h.svc.GetInstanceTimeline(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	if tasks == nil {
		tasks = []*model.Task{}
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (h *Handler) signalStep(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	id := r.PathValue("id")
	var signal model.StepSignal
	if err := json.NewDecoder(r.Body).Decode(&signal); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	signal.ActorID = r.Header.Get("X-User-ID")
	if err := h.svc.CompleteStep(r.Context(), tenantID, id, signal); err != nil {
		h.log.Error().Err(err).Msg("signal step")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "signaled"})
}

func (h *Handler) getDefinition(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	id := r.PathValue("id")
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID required")
		return
	}
	def, err := h.svc.GetDefinition(r.Context(), tenantID, id)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if def == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, def)
}

// ---- Visibility + grants (migration 000062) ------------------------------

type visibilityBody struct {
	Visibility string `json:"visibility"`
}

func (h *Handler) setDefinitionVisibility(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	id := r.PathValue("id")
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID required")
		return
	}
	var body visibilityBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	def, err := h.svc.SetDefinitionVisibility(r.Context(), tenantID, id, body.Visibility)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, def)
}

func (h *Handler) listDefinitionGrants(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	id := r.PathValue("id")
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID required")
		return
	}
	grants, err := h.svc.ListGrants(r.Context(), tenantID, id)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	if grants == nil {
		grants = []*model.WorkflowGrant{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"grants": grants})
}

type grantBody struct {
	GranteeType string `json:"grantee_type"`
	GranteeID   string `json:"grantee_id"`
}

func (h *Handler) addDefinitionGrant(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	id := r.PathValue("id")
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID required")
		return
	}
	var body grantBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	g, err := h.svc.AddGrant(r.Context(), tenantID, id, body.GranteeType, body.GranteeID)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

func (h *Handler) removeDefinitionGrant(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	id := r.PathValue("id")
	granteeType := r.PathValue("granteeType")
	granteeID := r.PathValue("granteeId")
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID required")
		return
	}
	if err := h.svc.RemoveGrant(r.Context(), tenantID, id, granteeType, granteeID); err != nil {
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "remove failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) updateDefinition(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	id := r.PathValue("id")
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID required")
		return
	}
	var body createDefBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		writeError(w, http.StatusBadRequest, "name required")
		return
	}
	def, err := h.svc.UpdateDefinition(r.Context(), tenantID, id, body.Name, body.Description, body.Steps)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, def)
}

func (h *Handler) deleteDefinition(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	id := r.PathValue("id")
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID required")
		return
	}
	if err := h.svc.DeleteDefinition(r.Context(), tenantID, id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listInstances supports the admin "Active instances" table.
// Query params:
//   - status=active     — non-terminal instances (default behaviour
//     for the admin page). Empty / any other value returns 400 today;
//     extend when the UI grows a "completed" filter.
func (h *Handler) listInstances(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID required")
		return
	}
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "active"
	}
	if status != "active" {
		writeError(w, http.StatusBadRequest, "only status=active is supported today")
		return
	}
	insts, err := h.svc.ListActiveInstances(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	if insts == nil {
		insts = []*model.WorkflowInstance{}
	}
	writeJSON(w, http.StatusOK, insts)
}

// getActiveInstanceByDocument is the per-document Workflow tab read.
// Returns {instance, tasks} bundle so the FE renders the timeline
// without a second round trip. 204 (no body) when the document has
// no active workflow — distinct from 404 "document doesn't exist"
// which the doc service handles.
func (h *Handler) getActiveInstanceByDocument(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	docID := r.PathValue("document_id")
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID required")
		return
	}
	inst, tasks, err := h.svc.GetActiveInstanceByDocument(r.Context(), tenantID, docID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if inst == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if tasks == nil {
		tasks = []*model.Task{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"instance": inst, "tasks": tasks})
}

func (h *Handler) cancelInstance(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	id := r.PathValue("id")
	if err := h.svc.CancelInstance(r.Context(), tenantID, id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

func (h *Handler) listMyTasks(w http.ResponseWriter, r *http.Request) {
	// Back-compat shim: /tasks/mine always returns pending tasks for the
	// caller. New clients should use /tasks?assignee=me&status=pending.
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	userID := r.Header.Get("X-User-ID")
	tasks, err := h.svc.ListTasks(r.Context(), tenantID, userID, "pending")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	if tasks == nil {
		tasks = []*model.Task{}
	}
	writeJSON(w, http.StatusOK, tasks)
}

// listTasks is the Wave 7 Prompt 7.4 read endpoint.
//
// Query params:
//   - assignee: user UUID, or the literal "me" (resolves to X-User-ID).
//     Default: "me".
//   - status:   one of pending, completed, rejected, delegated,
//     escalated, skipped. Empty = all statuses.
//
// Returns [] (never null) to simplify frontend consumption.
func (h *Handler) listTasks(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	userID := r.Header.Get("X-User-ID")
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID and X-User-ID required")
		return
	}
	assignee := r.URL.Query().Get("assignee")
	if assignee == "" || assignee == "me" {
		assignee = userID
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "", "pending", "completed", "rejected", "delegated", "escalated", "skipped", "in_progress":
	default:
		writeError(w, http.StatusBadRequest, "invalid status")
		return
	}
	tasks, err := h.svc.ListTasks(r.Context(), tenantID, assignee, status)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	if tasks == nil {
		tasks = []*model.Task{}
	}
	writeJSON(w, http.StatusOK, tasks)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
