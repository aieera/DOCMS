// version_label_handler — set/clear the optional human-friendly label
// on a document version ("Q1 final", "Approved for legal review", etc).
//
//	PATCH /api/v1/documents/{document_id}/versions/{version_id}/label
//	body: { "label": "Q1 final" }
//
// Empty string clears the label. Authorization mirrors the document
// title-update rule (requires "edit" capability on the document);
// legal hold permits update_metadata so labels can be edited while
// the document is held.
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/service"
)

// VersionLabelHandler exposes the label PATCH endpoint.
type VersionLabelHandler struct {
	svc *service.DocumentService
}

// NewVersionLabelHandler constructs the handler.
func NewVersionLabelHandler(svc *service.DocumentService) *VersionLabelHandler {
	return &VersionLabelHandler{svc: svc}
}

// Register mounts the route on the supplied mux.
func (h *VersionLabelHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("PATCH /api/v1/documents/{document_id}/versions/{version_id}/label", h.set)
}

func (h *VersionLabelHandler) set(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	docID, err := uuid.Parse(r.PathValue("document_id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("document_id", "not a uuid"))
		return
	}
	verID, err := uuid.Parse(r.PathValue("version_id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("version_id", "not a uuid"))
		return
	}
	var body struct {
		Label string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	v, err := h.svc.SetVersionLabel(r.Context(), docID, verID, body.Label)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":             v.ID.String(),
		"document_id":    v.DocumentID.String(),
		"version_number": v.VersionNumber,
		"label":          v.Label,
	})
}
