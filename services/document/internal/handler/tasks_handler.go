// ADR 0068 — lightweight tasks REST surface.
//
//	POST   /api/v1/tasks                         create
//	GET    /api/v1/tasks/mine                    inbox
//	GET    /api/v1/tasks                         list (?document_id, ?status, ?include_completed)
//	GET    /api/v1/tasks/{id}                    read one
//	PATCH  /api/v1/tasks/{id}                    edit fields
//	POST   /api/v1/tasks/{id}/assign      {assignee_id}
//	POST   /api/v1/tasks/{id}/unassign
//	POST   /api/v1/tasks/{id}/complete
//	POST   /api/v1/tasks/{id}/reopen
//	POST   /api/v1/tasks/{id}/cancel
//	DELETE /api/v1/tasks/{id}
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// TasksHandler mounts the /tasks/* routes.
type TasksHandler struct {
	svc *service.DocumentService
	log zerolog.Logger
}

// NewTasksHandler constructs the handler.
func NewTasksHandler(svc *service.DocumentService, log zerolog.Logger) *TasksHandler {
	return &TasksHandler{svc: svc, log: log}
}

// Register attaches the routes.
func (h *TasksHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/tasks",                 h.create)
	mux.HandleFunc("GET /api/v1/tasks/mine",             h.listMine)
	mux.HandleFunc("GET /api/v1/tasks",                  h.list)
	mux.HandleFunc("GET /api/v1/tasks/{id}",             h.get)
	mux.HandleFunc("PATCH /api/v1/tasks/{id}",           h.update)
	mux.HandleFunc("POST /api/v1/tasks/{id}/assign",     h.assign)
	mux.HandleFunc("POST /api/v1/tasks/{id}/unassign",   h.unassign)
	mux.HandleFunc("POST /api/v1/tasks/{id}/complete",   h.complete)
	mux.HandleFunc("POST /api/v1/tasks/{id}/reopen",     h.reopen)
	mux.HandleFunc("POST /api/v1/tasks/{id}/cancel",     h.cancel)
	mux.HandleFunc("DELETE /api/v1/tasks/{id}",          h.delete)
}

// ---- bodies -------------------------------------------------------------

type createTaskBody struct {
	Title                    string  `json:"title"`
	Description              string  `json:"description,omitempty"`
	Priority                 string  `json:"priority,omitempty"`
	DueAt                    *string `json:"due_at,omitempty"`
	AssigneeID               *string `json:"assignee_id,omitempty"`
	LinkedDocumentID         *string `json:"linked_document_id,omitempty"`
	LinkedWorkflowInstanceID *string `json:"linked_workflow_instance_id,omitempty"`
}

type updateTaskBody struct {
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	Priority    *string `json:"priority,omitempty"`
	DueAt       *string `json:"due_at,omitempty"`
	ClearDueAt  bool    `json:"clear_due_at,omitempty"`
}

type assignBody struct {
	AssigneeID string `json:"assignee_id"`
}

// ---- handlers -----------------------------------------------------------

func (h *TasksHandler) create(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	var body createTaskBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	in := service.CreateTaskInput{
		Title: body.Title, Description: body.Description, Priority: body.Priority,
	}
	if body.DueAt != nil && *body.DueAt != "" {
		t, err := time.Parse(time.RFC3339, *body.DueAt)
		if err != nil {
			writeErr(w, r, vdmserr.Validation("due_at", "must be RFC3339"))
			return
		}
		in.DueAt = &t
	}
	if id, ok := parseOptUUID(w, r, "assignee_id", body.AssigneeID); !ok {
		return
	} else if id != nil {
		in.AssigneeID = id
	}
	if id, ok := parseOptUUID(w, r, "linked_document_id", body.LinkedDocumentID); !ok {
		return
	} else if id != nil {
		in.LinkedDocumentID = id
	}
	if id, ok := parseOptUUID(w, r, "linked_workflow_instance_id", body.LinkedWorkflowInstanceID); !ok {
		return
	} else if id != nil {
		in.LinkedWorkflowInstanceID = id
	}
	t, err := h.svc.CreateTask(ctx, in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, t)
}

func (h *TasksHandler) listMine(w http.ResponseWriter, r *http.Request) {
	ctx, _, userID, ok := authedContext(w, r)
	if !ok {
		return
	}
	uid := userID
	rows, err := h.svc.ListTasks(ctx, repository.TaskFilters{
		AssigneeID:       &uid,
		IncludeCompleted: r.URL.Query().Get("include_completed") == "true",
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if rows == nil {
		rows = []repository.Task{}
	}
	writeJSONStatus(w, http.StatusOK, rows)
}

func (h *TasksHandler) list(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	f := repository.TaskFilters{
		IncludeCompleted: r.URL.Query().Get("include_completed") == "true",
	}
	if did := r.URL.Query().Get("document_id"); did != "" {
		dID, err := uuid.Parse(did)
		if err != nil {
			writeErr(w, r, vdmserr.Validation("document_id", "invalid uuid"))
			return
		}
		f.LinkedDocumentID = &dID
	}
	if st := r.URL.Query().Get("status"); st != "" {
		f.Statuses = []string{st}
	}
	rows, err := h.svc.ListTasks(ctx, f)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if rows == nil {
		rows = []repository.Task{}
	}
	writeJSONStatus(w, http.StatusOK, rows)
}

func (h *TasksHandler) get(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	t, err := h.svc.GetTask(ctx, id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, t)
}

func (h *TasksHandler) update(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	var body updateTaskBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	in := service.UpdateTaskInput{
		ID: id, Title: body.Title, Description: body.Description,
		Priority: body.Priority, ClearDueAt: body.ClearDueAt,
	}
	if body.DueAt != nil && *body.DueAt != "" {
		t, err := time.Parse(time.RFC3339, *body.DueAt)
		if err != nil {
			writeErr(w, r, vdmserr.Validation("due_at", "must be RFC3339"))
			return
		}
		in.DueAt = &t
	}
	t, err := h.svc.UpdateTask(ctx, in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, t)
}

func (h *TasksHandler) assign(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	var body assignBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	aid, err := uuid.Parse(body.AssigneeID)
	if err != nil {
		writeErr(w, r, vdmserr.Validation("assignee_id", "invalid uuid"))
		return
	}
	t, err := h.svc.AssignTask(ctx, id, aid)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, t)
}

func (h *TasksHandler) unassign(w http.ResponseWriter, r *http.Request) {
	h.statusOp(w, r, h.svc.UnassignTask)
}
func (h *TasksHandler) complete(w http.ResponseWriter, r *http.Request) {
	h.statusOp(w, r, h.svc.CompleteTask)
}
func (h *TasksHandler) reopen(w http.ResponseWriter, r *http.Request) {
	h.statusOp(w, r, h.svc.ReopenTask)
}
func (h *TasksHandler) cancel(w http.ResponseWriter, r *http.Request) {
	h.statusOp(w, r, h.svc.CancelTask)
}

// statusOp factors the small handlers that just resolve the path id
// and call a single service method that takes ctx + id.
func (h *TasksHandler) statusOp(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, id uuid.UUID) (*repository.Task, error)) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	t, err := fn(ctx, id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, t)
}

func (h *TasksHandler) delete(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	if err := h.svc.DeleteTask(ctx, id); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- helpers ------------------------------------------------------------

func parseOptUUID(w http.ResponseWriter, r *http.Request, field string, raw *string) (*uuid.UUID, bool) {
	if raw == nil || *raw == "" {
		return nil, true
	}
	id, err := uuid.Parse(*raw)
	if err != nil {
		writeErr(w, r, vdmserr.Validation(field, "invalid uuid"))
		return nil, false
	}
	return &id, true
}

