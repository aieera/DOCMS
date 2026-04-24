// Package handler exposes REST endpoints for the workflow service.
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/rs/zerolog"
	"go.temporal.io/sdk/client"

	"github.com/vaultdms/vaultdms/services/workflow/internal/model"
	"github.com/vaultdms/vaultdms/services/workflow/internal/service"
)

// Handler holds HTTP route handlers.
type Handler struct {
	svc *service.Service
	log zerolog.Logger
	// Temporal client used by the platform /schedules endpoint. Kept
	// optional (nil-safe) — deployments without a Temporal wiring can
	// still boot and serve non-platform routes.
	tc client.Client
}

// New constructs a Handler.
func New(svc *service.Service, log zerolog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

// WithTemporal returns a copy of the Handler with the Temporal client
// wired. Call before RegisterPlatform so the /schedules endpoint can
// enumerate schedules.
func (h *Handler) WithTemporal(tc client.Client) *Handler {
	h.tc = tc
	return h
}

// Register mounts routes.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/workflows/definitions", h.listDefinitions)
	mux.HandleFunc("POST /api/v1/workflows/definitions", h.createDefinition)
	mux.HandleFunc("POST /api/v1/workflows/instances", h.startInstance)
	mux.HandleFunc("GET /api/v1/workflows/instances/{id}", h.getInstance)
	mux.HandleFunc("POST /api/v1/workflows/instances/{id}/signal", h.signalStep)
	mux.HandleFunc("POST /api/v1/workflows/instances/{id}/cancel", h.cancelInstance)
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
		h.log.Error().Err(err).Str("path", r.URL.Path).Msg("list failed")
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	writeJSON(w, http.StatusOK, defs)
}

type createDefBody struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Steps       []model.Step `json:"steps"`
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
	def, err := h.svc.CreateDefinition(r.Context(), tenantID, body.Name, body.Description, userID, body.Steps)
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
		h.log.Error().Err(err).Str("path", r.URL.Path).Msg("list failed")
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
		h.log.Error().Err(err).Str("path", r.URL.Path).Msg("list failed")
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
