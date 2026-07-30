// Package handler is the task service's HTTP surface (Task 7,
// 2026-07-28 task-service design): task CRUD + status transitions +
// assignee/document link management (tasks_handler.go), comments + the
// activity feed (comments_handler.go), wired together here. Every route
// sits behind middleware.SessionAuth (see cmd/server/main.go) — no
// per-route session verification happens in this package, only
// resolving the auth.UserInfo SessionAuth already placed on ctx.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/task/internal/service"
)

// Handler groups every task-service HTTP route. svc carries the
// business logic (validation, tenant/permission gates, transactional
// outbox emission); this package's job is purely wire-shape translation
// plus the ErrValidation/ErrForbidden/ErrNotFound -> 400/403/404
// mapping every handler shares.
type Handler struct {
	svc *service.TaskService
	log zerolog.Logger
}

// New constructs a Handler. Signature is unchanged from Task 1's
// placeholder so cmd/server/main.go's call site
// (`handler.New(svc, *log.Z())`) keeps compiling without edits.
func New(svc *service.TaskService, log zerolog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

// Register wires every route this service exposes onto mux. Literal
// segments ("/tasks/mine", "/tasks/created") take precedence over the
// "{id}" wildcard under Go 1.22's http.ServeMux regardless of
// registration order, so ordering here is for readability only.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/tasks", h.createTask)
	mux.HandleFunc("GET /api/v1/tasks/mine", h.listMine)
	mux.HandleFunc("GET /api/v1/tasks/created", h.listCreated)
	mux.HandleFunc("GET /api/v1/tasks", h.listTasks)
	mux.HandleFunc("GET /api/v1/tasks/{id}", h.getTask)
	mux.HandleFunc("PATCH /api/v1/tasks/{id}", h.updateTask)
	mux.HandleFunc("POST /api/v1/tasks/{id}/start", h.startTask)
	mux.HandleFunc("POST /api/v1/tasks/{id}/complete", h.completeTask)
	mux.HandleFunc("POST /api/v1/tasks/{id}/reopen", h.reopenTask)
	mux.HandleFunc("POST /api/v1/tasks/{id}/cancel", h.cancelTask)
	mux.HandleFunc("DELETE /api/v1/tasks/{id}", h.deleteTask)
	mux.HandleFunc("POST /api/v1/tasks/{id}/assignees", h.addAssignee)
	mux.HandleFunc("DELETE /api/v1/tasks/{id}/assignees/{userId}", h.removeAssignee)
	mux.HandleFunc("POST /api/v1/tasks/{id}/documents", h.linkDocument)
	mux.HandleFunc("DELETE /api/v1/tasks/{id}/documents/{docId}", h.unlinkDocument)
	mux.HandleFunc("GET /api/v1/tasks/{id}/comments", h.listComments)
	mux.HandleFunc("POST /api/v1/tasks/{id}/comments", h.addComment)
	mux.HandleFunc("PATCH /api/v1/tasks/{id}/comments/{cid}", h.updateComment)
	mux.HandleFunc("DELETE /api/v1/tasks/{id}/comments/{cid}", h.deleteComment)
	mux.HandleFunc("GET /api/v1/tasks/{id}/activity", h.listActivity)
}

// ---- shared helpers --------------------------------------------------

// tenantOrUnauthorized resolves the tenant id middleware.SessionAuth
// placed on ctx, writing 401 {"error":"no tenant"} and returning
// ok=false if absent — the binding contract's explicit "auth.GetTenantID
// failure -> 401" rule, ported from the document service's
// clause_matches_handler.go guard clause.
func tenantOrUnauthorized(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	tid, err := auth.GetTenantID(r.Context())
	if err != nil || tid == uuid.Nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no tenant"})
		return uuid.Nil, false
	}
	return tid, true
}

// pathUUID parses the named path wildcard as a uuid, writing 400 on
// failure so callers can `if !ok { return }` in one line.
func pathUUID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid " + name})
		return uuid.Nil, false
	}
	return id, true
}

// decodeJSON decodes r.Body into v, writing 400 on malformed JSON.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeServiceError maps a TaskService error onto this package's wire
// contract: ErrValidation -> 400, ErrForbidden -> 403,
// ErrNotFound/pgx.ErrNoRows -> 404, anything else -> 500. The 400/403
// bodies surface the sentinel's wrapped message (that's the whole point
// of ErrValidation/ErrForbidden — they're already user-facing text); a
// 500 never does — it's logged server-side with the route for triage and
// the client gets a generic body, so an internal error's raw text (which
// could carry a SQL fragment or similar) never reaches the wire.
func (h *Handler) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrValidation):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, service.ErrForbidden):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
	case errors.Is(err, service.ErrNotFound), errors.Is(err, pgx.ErrNoRows):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	default:
		h.log.Error().Err(err).Str("method", r.Method).Str("path", r.URL.Path).Msg("task handler 5xx")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
	}
}
