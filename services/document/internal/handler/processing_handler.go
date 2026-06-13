// Document processing-status surface (ADR 0115).
//
//	GET  /api/v1/documents/{id}/processing   — per-stage status + roll-up
//	POST /api/v1/documents/{id}/reprocess     — replay the intelligence
//	                                            pipeline for the current
//	                                            version (owner|admin|
//	                                            compliance_officer)
//
// The intelligence workers write document_processing_stages rows as each
// stage runs/completes/fails; this surface lets the UI render "processing
// failed because X, retry" instead of empty tabs.
package handler

import (
	"net/http"

	"github.com/aieera/sedoc/services/document/internal/service"
)

type ProcessingHandler struct {
	svc *service.DocumentService
}

func NewProcessingHandler(svc *service.DocumentService) *ProcessingHandler {
	return &ProcessingHandler{svc: svc}
}

func (h *ProcessingHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/documents/{id}/processing", h.status)
	mux.HandleFunc("POST /api/v1/documents/{id}/reprocess", h.reprocess)
}

func (h *ProcessingHandler) status(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	docID, err := parseUUID("id", r.PathValue("id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out, err := h.svc.GetProcessingStages(ctx, docID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, out)
}

func (h *ProcessingHandler) reprocess(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin", "compliance_officer") {
		return
	}
	docID, err := parseUUID("id", r.PathValue("id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := h.svc.ReprocessDocument(ctx, docID); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, map[string]string{"status": "reprocessing"})
}
