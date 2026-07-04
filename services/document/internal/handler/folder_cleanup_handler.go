// folder_cleanup_handler — admin-only maintenance surface for removing
// empty leaf folders (the orphaned "b005/b169"-style debris that
// accumulates from aborted ingests).
//
//	GET  /api/v1/admin/folders/empty    — dry-run: list what WOULD be removed
//	POST /api/v1/admin/folders/cleanup  — soft-delete empty folders (recoverable)
//
// Both routes require owner/admin role. Deletions are soft (land in
// Trash) and emit dms.folder.deleted.v1 so the action is audited.
package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// FolderCleanupHandler exposes the empty-folder maintenance surface.
type FolderCleanupHandler struct {
	svc *service.DocumentService
}

// NewFolderCleanupHandler constructs the handler.
func NewFolderCleanupHandler(svc *service.DocumentService) *FolderCleanupHandler {
	return &FolderCleanupHandler{svc: svc}
}

// Register mounts the routes on the supplied mux.
func (h *FolderCleanupHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/folders/empty", h.list)
	mux.HandleFunc("POST /api/v1/admin/folders/cleanup", h.cleanup)
}

type emptyFolderEntry struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	WorkspaceID string    `json:"workspace_id"`
	Path        string    `json:"path"`
	Depth       int       `json:"depth"`
	CreatedAt   time.Time `json:"created_at"`
}

type emptyFolderListResponse struct {
	Items []emptyFolderEntry `json:"items"`
	Count int                `json:"count"`
	// Truncated is true when the result hit the page limit — there may
	// be more empty folders than shown.
	Truncated bool `json:"truncated"`
}

type cleanupResponse struct {
	Deleted       int                `json:"deleted"`
	MoreRemaining bool               `json:"more_remaining"`
	Items         []emptyFolderEntry `json:"items"`
}

// optionalWorkspaceID parses ?workspace_id= — empty means "all
// workspaces" (uuid.Nil). A malformed value is a 400.
func optionalWorkspaceID(r *http.Request) (uuid.UUID, error) {
	v := r.URL.Query().Get("workspace_id")
	if v == "" {
		return uuid.Nil, nil
	}
	id, err := uuid.Parse(v)
	if err != nil {
		return uuid.Nil, vdmserr.Validation("workspace_id", "not a uuid")
	}
	return id, nil
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func (h *FolderCleanupHandler) list(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	ws, err := optionalWorkspaceID(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	olderThanDays := atoiDefault(r.URL.Query().Get("older_than_days"), 0)
	limit := atoiDefault(r.URL.Query().Get("limit"), 200)
	folders, err := h.svc.ListEmptyFolders(ctx, ws, olderThanDays, limit)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	items := emptyFoldersToEntries(folders)
	writeJSONStatus(w, http.StatusOK, emptyFolderListResponse{
		Items:     items,
		Count:     len(items),
		Truncated: len(folders) == limit,
	})
}

type cleanupRequest struct {
	WorkspaceID   string `json:"workspace_id"`
	OlderThanDays int    `json:"older_than_days"`
	Max           int    `json:"max"`
}

func (h *FolderCleanupHandler) cleanup(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	var body cleanupRequest
	// Body is optional — an empty POST cleans the whole tenant with
	// defaults. Reject only malformed JSON.
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err.Error() != "EOF" {
			writeErr(w, r, vdmserr.Validation("body", "invalid json"))
			return
		}
	}
	ws := uuid.Nil
	if body.WorkspaceID != "" {
		id, perr := uuid.Parse(body.WorkspaceID)
		if perr != nil {
			writeErr(w, r, vdmserr.Validation("workspace_id", "not a uuid"))
			return
		}
		ws = id
	}
	res, err := h.svc.CleanupEmptyFolders(ctx, ws, body.OlderThanDays, body.Max)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, cleanupResponse{
		Deleted:       res.Deleted,
		MoreRemaining: res.MoreRemaining,
		Items:         emptyFoldersToEntries(res.Folders),
	})
}

func emptyFoldersToEntries(folders []model.Folder) []emptyFolderEntry {
	items := make([]emptyFolderEntry, 0, len(folders))
	for i := range folders {
		f := &folders[i]
		items = append(items, emptyFolderEntry{
			ID:          f.ID.String(),
			Name:        f.Name,
			WorkspaceID: f.WorkspaceID.String(),
			Path:        f.Path,
			Depth:       f.Depth,
			CreatedAt:   f.CreatedAt,
		})
	}
	return items
}
