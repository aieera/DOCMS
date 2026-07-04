// Note / wiki creation.
//
//	POST /api/v1/notes   {workspace_id, folder_id, title?, doc_type?}
//
// A note is a normal document (doc_type = note|wiki) created without a file
// upload — its content is a collaborative markdown version added later
// through the version path, so it inherits versioning, ACL, search, and
// audit. Kept as a thin REST surface (not the gRPC create path) so it needs
// no proto change. SessionAuth populates the caller; CreateNote enforces the
// folder "edit" permission.
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/service"
)

type NotesHandler struct {
	svc *service.DocumentService
	log zerolog.Logger
}

func NewNotesHandler(svc *service.DocumentService, log zerolog.Logger) *NotesHandler {
	return &NotesHandler{svc: svc, log: log}
}

func (h *NotesHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/notes", h.create)
}

func (h *NotesHandler) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WorkspaceID string `json:"workspace_id"`
		FolderID    string `json:"folder_id"`
		Title       string `json:"title"`
		DocType     string `json:"doc_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid JSON"))
		return
	}
	wsID, err := uuid.Parse(body.WorkspaceID)
	if err != nil {
		writeErr(w, r, vdmserr.Validation("workspace_id", "invalid uuid"))
		return
	}
	folderID, err := uuid.Parse(body.FolderID)
	if err != nil {
		writeErr(w, r, vdmserr.Validation("folder_id", "invalid uuid"))
		return
	}
	doc, err := h.svc.CreateNote(r.Context(), service.CreateNoteInput{
		WorkspaceID: wsID,
		FolderID:    folderID,
		Title:       body.Title,
		DocType:     body.DocType,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	// Minimal snake_case projection — enough for the client to navigate to
	// the new note. model.Document has no JSON tags, so we don't serialise
	// it directly.
	writeJSONStatus(w, http.StatusCreated, map[string]any{
		"id":              doc.ID.String(),
		"workspace_id":    doc.WorkspaceID.String(),
		"folder_id":       doc.FolderID.String(),
		"title":           doc.Title,
		"doc_type":        doc.DocType,
		"lifecycle_state": string(doc.LifecycleState),
	})
}
