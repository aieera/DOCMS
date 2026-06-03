// §17.3 / D10 — annotation REST endpoints.
//
//	POST   /api/v1/documents/{id}/versions/{vid}/annotations
//	GET    /api/v1/documents/{id}/versions/{vid}/annotations
//	PATCH  /api/v1/annotations/{id}
//	DELETE /api/v1/annotations/{id}
//
// Real-time fan-out (collaboration WS) consumes the dms.annotation.*
// outbox events the service layer emits — not this file's concern.
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// AnnotationsHandler wires the four annotation routes onto a mux.
type AnnotationsHandler struct {
	svc *service.DocumentService
	log zerolog.Logger
}

// NewAnnotationsHandler constructs the handler.
func NewAnnotationsHandler(svc *service.DocumentService, log zerolog.Logger) *AnnotationsHandler {
	return &AnnotationsHandler{svc: svc, log: log}
}

// Register mounts the endpoints. Uses Go 1.22 pattern syntax matching
// the compliance/privacy handlers for consistency.
func (h *AnnotationsHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/documents/{id}/versions/{vid}/annotations", h.create)
	mux.HandleFunc("GET /api/v1/documents/{id}/versions/{vid}/annotations", h.list)
	mux.HandleFunc("PATCH /api/v1/annotations/{id}", h.update)
	mux.HandleFunc("DELETE /api/v1/annotations/{id}", h.delete)
}

// ---- bodies --------------------------------------------------------

type annotationCreateBody struct {
	Page int            `json:"page"`
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

type annotationUpdateBody struct {
	Page int            `json:"page"`
	Data map[string]any `json:"data"`
}

// ---- handlers ------------------------------------------------------

func (h *AnnotationsHandler) create(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	versionID, err := uuid.Parse(r.PathValue("vid"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("vid", "invalid uuid"))
		return
	}
	var body annotationCreateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}

	ctx := r.Context()
	ann, err := h.svc.CreateAnnotation(ctx, &service.CreateAnnotationInput{
		DocumentID: docID,
		VersionID:  versionID,
		Page:       body.Page,
		Type:       body.Type,
		Data:       body.Data,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, ann)
}

func (h *AnnotationsHandler) list(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	versionID, err := uuid.Parse(r.PathValue("vid"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("vid", "invalid uuid"))
		return
	}
	ctx := r.Context()
	items, err := h.svc.ListAnnotations(ctx, docID, versionID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	// `items` may be nil when there are no rows; the frontend expects
	// an array, not null.
	if items == nil {
		items = []model.Annotation{}
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"annotations": items})
}

func (h *AnnotationsHandler) update(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	var body annotationUpdateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	ctx := r.Context()
	ann, err := h.svc.UpdateAnnotation(ctx, &service.UpdateAnnotationInput{
		ID:   id,
		Page: body.Page,
		Data: body.Data,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, ann)
}

func (h *AnnotationsHandler) delete(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	ctx := r.Context()
	if err := h.svc.DeleteAnnotation(ctx, id); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
