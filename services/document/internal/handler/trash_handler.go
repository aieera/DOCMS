// trash_handler — admin-only Trash surface for soft-deleted documents.
//
//	GET    /api/v1/admin/trash                       — list soft-deleted docs
//	POST   /api/v1/admin/trash/{id}/restore          — clear deleted_at
//	DELETE /api/v1/admin/trash/{id}                  — purge (S3 + DB)
//
// All three routes require owner/admin role. Authedcontext stamps the
// session role onto ctx so the service layer's OPA checks (Rule 6)
// fire identically to the rest of the admin surface.
package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
	"github.com/vaultdms/vaultdms/services/document/internal/service"
)

// TrashHandler exposes the admin Trash surface.
type TrashHandler struct {
	svc *service.DocumentService
}

// NewTrashHandler constructs the handler.
func NewTrashHandler(svc *service.DocumentService) *TrashHandler {
	return &TrashHandler{svc: svc}
}

// Register mounts the three routes on the supplied mux.
func (h *TrashHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/trash", h.list)
	mux.HandleFunc("POST /api/v1/admin/trash/{id}/restore", h.restore)
	mux.HandleFunc("DELETE /api/v1/admin/trash/{id}", h.purge)
}

// trashEntry is the JSON shape returned to the admin UI. Flat,
// renderable as a single row in the Trash table without follow-up
// fetches.
type trashEntry struct {
	ID             string     `json:"id"`
	Title          string     `json:"title"`
	WorkspaceID    string     `json:"workspace_id"`
	FolderID       string     `json:"folder_id,omitempty"`
	MimeType       string     `json:"mime_type,omitempty"`
	TotalSizeBytes int64      `json:"total_size_bytes"`
	LifecycleState string     `json:"lifecycle_state"`
	CreatedBy      string     `json:"created_by,omitempty"`
	CreatedByName  string     `json:"created_by_name,omitempty"`
	DeletedAt      *time.Time `json:"deleted_at,omitempty"`
}

type listResponse struct {
	Items         []trashEntry `json:"items"`
	NextPageToken string       `json:"next_page_token,omitempty"`
}

func (h *TrashHandler) list(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	pageSize := 50
	if v := r.URL.Query().Get("page_size"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			pageSize = n
		}
	}
	pageToken := r.URL.Query().Get("page_token")
	page, err := h.svc.ListTrash(ctx, pageSize, pageToken)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	items := make([]trashEntry, 0, len(page.Items))
	for i := range page.Items {
		items = append(items, trashFromModel(&page.Items[i]))
	}
	writeJSONStatus(w, http.StatusOK, listResponse{
		Items:         items,
		NextPageToken: page.NextPageToken,
	})
}

func (h *TrashHandler) restore(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	if err := h.svc.RestoreDocument(ctx, docID); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *TrashHandler) purge(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	if err := h.svc.PurgeDocument(ctx, docID); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func trashFromModel(d *model.Document) trashEntry {
	out := trashEntry{
		ID:             d.ID.String(),
		Title:          d.Title,
		WorkspaceID:    d.WorkspaceID.String(),
		MimeType:       d.MimeType,
		TotalSizeBytes: d.TotalSizeBytes,
		LifecycleState: string(d.LifecycleState),
		CreatedBy:      d.CreatedBy.String(),
		CreatedByName:  d.CreatedByName,
		DeletedAt:      d.DeletedAt,
	}
	if d.FolderID != uuid.Nil {
		out.FolderID = d.FolderID.String()
	}
	return out
}

// _ ensures encoding/json is imported even if no struct tags would
// otherwise force the dependency in some refactor.
var _ = json.Marshal
