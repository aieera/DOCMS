// Task 7 (2026-07-28 task-service design) — comment + activity-feed
// routes:
//
//	GET    /api/v1/tasks/{id}/comments
//	POST   /api/v1/tasks/{id}/comments
//	PATCH  /api/v1/tasks/{id}/comments/{cid}
//	DELETE /api/v1/tasks/{id}/comments/{cid}
//	GET    /api/v1/tasks/{id}/activity
//
// Both list endpoints return a bare JSON array (the underlying service
// methods — ListComments/ListActivity — already return plain slices, not
// a Page[T] envelope, so there's no pagination metadata to wrap them
// in). Shared plumbing lives in handler.go.
package handler

import (
	"net/http"

	"github.com/aieera/sedoc/services/task/internal/model"
)

type commentBody struct {
	Body string `json:"body"`
}

func (h *Handler) listComments(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	items, err := h.svc.ListComments(r.Context(), id, parseIntParam(r, "limit"), parseIntParam(r, "offset"))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	if items == nil {
		items = []model.TaskComment{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *Handler) addComment(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	var body commentBody
	if !decodeJSON(w, r, &body) {
		return
	}
	c, err := h.svc.AddComment(r.Context(), id, body.Body)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (h *Handler) updateComment(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	cid, ok := pathUUID(w, r, "cid")
	if !ok {
		return
	}
	var body commentBody
	if !decodeJSON(w, r, &body) {
		return
	}
	c, err := h.svc.UpdateComment(r.Context(), id, cid, body.Body)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (h *Handler) deleteComment(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	cid, ok := pathUUID(w, r, "cid")
	if !ok {
		return
	}
	if err := h.svc.DeleteComment(r.Context(), id, cid); err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listActivity(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantOrUnauthorized(w, r); !ok {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	items, err := h.svc.ListActivity(r.Context(), id, parseIntParam(r, "limit"), parseIntParam(r, "offset"))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	if items == nil {
		items = []model.TaskActivity{}
	}
	writeJSON(w, http.StatusOK, items)
}
