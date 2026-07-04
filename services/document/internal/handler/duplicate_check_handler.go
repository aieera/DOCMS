// duplicate_check_handler — pre-upload "possible duplicate" lookup.
//
//	GET /api/v1/documents/duplicates?sha256=<hex>&workspace_id=<uuid>
//
// Returns existing documents whose content matches the given sha256, so
// the upload UI can warn before creating a redundant document row. Any
// authenticated user; the service permission-filters matches to folders
// the caller can access.
package handler

import (
	"net/http"

	"github.com/google/uuid"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// DuplicateCheckHandler exposes the pre-upload duplicate lookup.
type DuplicateCheckHandler struct {
	svc *service.DocumentService
}

// NewDuplicateCheckHandler constructs the handler.
func NewDuplicateCheckHandler(svc *service.DocumentService) *DuplicateCheckHandler {
	return &DuplicateCheckHandler{svc: svc}
}

// Register mounts the route on the supplied mux.
func (h *DuplicateCheckHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/documents/duplicates", h.check)
}

type duplicateMatchEntry struct {
	DocumentID  string `json:"document_id"`
	Title       string `json:"title"`
	WorkspaceID string `json:"workspace_id"`
	FolderID    string `json:"folder_id,omitempty"`
	CreatedAt   string `json:"created_at"`
}

type duplicateCheckResponse struct {
	Matches []duplicateMatchEntry `json:"matches"`
	Count   int                   `json:"count"`
}

func (h *DuplicateCheckHandler) check(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	sha := r.URL.Query().Get("sha256")
	if sha == "" {
		writeErr(w, r, vdmserr.Validation("sha256", "required"))
		return
	}
	ws := uuid.Nil
	if v := r.URL.Query().Get("workspace_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			writeErr(w, r, vdmserr.Validation("workspace_id", "not a uuid"))
			return
		}
		ws = id
	}
	matches, err := h.svc.FindDuplicates(ctx, ws, sha)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := make([]duplicateMatchEntry, 0, len(matches))
	for i := range matches {
		m := &matches[i]
		entry := duplicateMatchEntry{
			DocumentID:  m.DocumentID.String(),
			Title:       m.Title,
			WorkspaceID: m.WorkspaceID.String(),
			CreatedAt:   m.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		}
		if m.FolderID != uuid.Nil {
			entry.FolderID = m.FolderID.String()
		}
		out = append(out, entry)
	}
	writeJSONStatus(w, http.StatusOK, duplicateCheckResponse{Matches: out, Count: len(out)})
}
