// retention_exempt_handler — per-document retention exemption toggle.
//
//	POST /api/v1/documents/{id}/retention-exempt
//	body: { "exempt": bool, "reason": string }
//
// Distinct from legal hold. See migration 000054 + service.SetDocumentRetentionExempt
// for the semantic separation (litigation hold vs business retention waiver).
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// RetentionExemptHandler exposes the per-document exemption toggle.
type RetentionExemptHandler struct {
	svc *service.DocumentService
}

// NewRetentionExemptHandler constructs the handler.
func NewRetentionExemptHandler(svc *service.DocumentService) *RetentionExemptHandler {
	return &RetentionExemptHandler{svc: svc}
}

// Register mounts the route on the supplied mux.
func (h *RetentionExemptHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/documents/{id}/retention-exempt", h.set)
}

func (h *RetentionExemptHandler) set(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	var body struct {
		Exempt bool   `json:"exempt"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	if err := h.svc.SetDocumentRetentionExempt(r.Context(), &service.SetDocumentRetentionExemptInput{
		DocumentID: docID,
		Exempt:     body.Exempt,
		Reason:     body.Reason,
	}); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
