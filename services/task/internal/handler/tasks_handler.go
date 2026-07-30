// Task 7 (2026-07-28 task-service design) — task CRUD, status
// transitions, and assignee/document link routes:
//
//	POST   /api/v1/tasks
//	GET    /api/v1/tasks/mine                    ← bare array (compat)
//	GET    /api/v1/tasks/created                 ← bare array (compat)
//	GET    /api/v1/tasks                         ← {items,total,limit,offset}
//	GET    /api/v1/tasks/{id}
//	PATCH  /api/v1/tasks/{id}
//	POST   /api/v1/tasks/{id}/start
//	POST   /api/v1/tasks/{id}/complete
//	POST   /api/v1/tasks/{id}/reopen
//	POST   /api/v1/tasks/{id}/cancel
//	DELETE /api/v1/tasks/{id}
//	POST   /api/v1/tasks/{id}/assignees
//	DELETE /api/v1/tasks/{id}/assignees/{userId}
//	POST   /api/v1/tasks/{id}/documents
//	DELETE /api/v1/tasks/{id}/documents/{docId}
//
// Comments + activity live in comments_handler.go; shared plumbing
// (writeJSON, writeServiceError, tenantOrUnauthorized, pathUUID,
// decodeJSON) lives in handler.go.
package handler

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/services/task/internal/model"
	"github.com/aieera/sedoc/services/task/internal/repository"
	"github.com/aieera/sedoc/services/task/internal/service"
)

// createTaskBody is POST /tasks's request DTO. DueAt is a *string (not
// *time.Time) so a malformed value can be rejected with a clear 400
// instead of failing json.Decode with an opaque error; assignee/document
// ids are strings for the same reason (parsed + validated below, one bad
// uuid in the list -> 400 rather than a decode-time panic-shaped error).
type createTaskBody struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Priority    string   `json:"priority"`
	Source      string   `json:"source"`
	DueAt       *string  `json:"due_at"`
	AssigneeIDs []string `json:"assignee_ids"`
	DocumentIDs []string `json:"document_ids"`
}

type updateTaskBody struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Priority    *string `json:"priority"`
	DueAt       *string `json:"due_at"`
	ClearDueAt  bool    `json:"clear_due_at"`
}

type assigneeBody struct {
	UserID string `json:"user_id"`
}

type documentBody struct {
	DocumentID string `json:"document_id"`
}

// ---- create / read / update / delete -----------------------------------

func (h *Handler) createTask(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}
	var body createTaskBody
	if !decodeJSON(w, r, &body) {
		return
	}

	in := service.CreateTaskInput{
		Title: body.Title, Description: body.Description,
		Priority: body.Priority, Source: body.Source,
	}
	dueAt, ok := parseOptRFC3339(w, "due_at", body.DueAt)
	if !ok {
		return
	}
	in.DueAt = dueAt
	aids, ok := parseUUIDSlice(w, "assignee_ids", body.AssigneeIDs)
	if !ok {
		return
	}
	in.AssigneeIDs = aids
	dids, ok := parseUUIDSlice(w, "document_ids", body.DocumentIDs)
	if !ok {
		return
	}
	in.DocumentIDs = dids

	t, err := h.svc.CreateTask(r.Context(), in)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// listTasks handles GET /tasks: filter=mine|created|all (default all),
// status, priority, document_id, q, include_completed, limit, offset,
// sort. Returns the {items,total,limit,offset} envelope — Page[T]'s json
// tags already match the wire contract, so it's marshaled as-is.
func (h *Handler) listTasks(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}
	q := r.URL.Query()

	f := repository.TaskFilters{
		IncludeCompleted: q.Get("include_completed") == "true",
		Priority:         q.Get("priority"),
		Query:            q.Get("q"),
		Sort:             q.Get("sort"),
		Limit:            parseIntParam(r, "limit"),
		Offset:           parseIntParam(r, "offset"),
	}
	if st := q.Get("status"); st != "" {
		f.Statuses = []string{st}
	}
	if did := q.Get("document_id"); did != "" {
		id, err := uuid.Parse(did)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid document_id"})
			return
		}
		f.DocumentID = &id
	}

	page, err := h.svc.ListTasks(r.Context(), service.ListTasksInput{Filter: q.Get("filter"), TaskFilters: f})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	if page.Items == nil {
		page.Items = []model.Task{}
	}
	writeJSON(w, http.StatusOK, page)
}

// listMine is the back-compat "my inbox" shim (web/src/api/tasks.ts:52-67
// and mobile both parse a bare Task[]): forces filter=mine, honors
// include_completed, ignores every other list param.
func (h *Handler) listMine(w http.ResponseWriter, r *http.Request) {
	h.listBareArray(w, r, "mine")
}

// listCreated is the "created by me" shim, mirroring listMine but scoped
// to filter=created.
func (h *Handler) listCreated(w http.ResponseWriter, r *http.Request) {
	h.listBareArray(w, r, "created")
}

func (h *Handler) listBareArray(w http.ResponseWriter, r *http.Request, filter string) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}
	f := repository.TaskFilters{IncludeCompleted: r.URL.Query().Get("include_completed") == "true"}
	page, err := h.svc.ListTasks(r.Context(), service.ListTasksInput{Filter: filter, TaskFilters: f})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	items := page.Items
	if items == nil {
		items = []model.Task{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *Handler) getTask(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	t, err := h.svc.GetTask(r.Context(), id)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *Handler) updateTask(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	var body updateTaskBody
	if !decodeJSON(w, r, &body) {
		return
	}
	in := service.UpdateTaskInput{
		ID: id, Title: body.Title, Description: body.Description,
		Priority: body.Priority, ClearDueAt: body.ClearDueAt,
	}
	dueAt, ok := parseOptRFC3339(w, "due_at", body.DueAt)
	if !ok {
		return
	}
	in.DueAt = dueAt

	t, err := h.svc.UpdateTask(r.Context(), in)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *Handler) deleteTask(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	if err := h.svc.DeleteTask(r.Context(), id); err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- status transitions -------------------------------------------------

func (h *Handler) startTask(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, h.svc.StartTask)
}
func (h *Handler) completeTask(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, h.svc.CompleteTask)
}
func (h *Handler) reopenTask(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, h.svc.ReopenTask)
}
func (h *Handler) cancelTask(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, h.svc.CancelTask)
}

// transition factors the four transition handlers, which all share the
// exact same shape: resolve tenant + path id, call a service method
// taking just (ctx, id), map the result.
func (h *Handler) transition(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, id uuid.UUID) (*model.Task, error)) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	t, err := fn(r.Context(), id)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// ---- assignees / documents -----------------------------------------------

func (h *Handler) addAssignee(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	var body assigneeBody
	if !decodeJSON(w, r, &body) {
		return
	}
	uid, err := uuid.Parse(body.UserID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid user_id"})
		return
	}
	t, err := h.svc.AddAssignee(r.Context(), id, uid)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *Handler) removeAssignee(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	uid, ok := pathUUID(w, r, "userId")
	if !ok {
		return
	}
	t, err := h.svc.RemoveAssignee(r.Context(), id, uid)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *Handler) linkDocument(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	var body documentBody
	if !decodeJSON(w, r, &body) {
		return
	}
	did, err := uuid.Parse(body.DocumentID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid document_id"})
		return
	}
	t, err := h.svc.LinkDocument(r.Context(), id, did)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *Handler) unlinkDocument(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	did, ok := pathUUID(w, r, "docId")
	if !ok {
		return
	}
	t, err := h.svc.UnlinkDocument(r.Context(), id, did)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// ---- small parse helpers -------------------------------------------------

// parseOptRFC3339 parses raw (if non-nil and non-empty) as RFC3339,
// writing 400 on a malformed value. A nil or empty raw is "no value" —
// returns (nil, true), distinct from an explicit-but-invalid string.
func parseOptRFC3339(w http.ResponseWriter, field string, raw *string) (*time.Time, bool) {
	if raw == nil || *raw == "" {
		return nil, true
	}
	t, err := time.Parse(time.RFC3339, *raw)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": field + " must be RFC3339"})
		return nil, false
	}
	return &t, true
}

// parseUUIDSlice parses every element of ss as a uuid, writing 400 and
// returning ok=false on the first invalid one. A nil/empty ss returns
// (nil, true).
func parseUUIDSlice(w http.ResponseWriter, field string, ss []string) ([]uuid.UUID, bool) {
	if len(ss) == 0 {
		return nil, true
	}
	out := make([]uuid.UUID, 0, len(ss))
	for _, s := range ss {
		id, err := uuid.Parse(s)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid uuid in " + field})
			return nil, false
		}
		out = append(out, id)
	}
	return out, true
}

// parseIntParam reads query param name as an int, defaulting to 0 (which
// the service layer's normalizeLimitOffset then maps to its own
// defaults) on absence or a malformed value — limit/offset aren't
// documented as 400-on-invalid in the binding contract, so a bad value
// degrades to "use the default" rather than rejecting the request.
func parseIntParam(r *http.Request, name string) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}
