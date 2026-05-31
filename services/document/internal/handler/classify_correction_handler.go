// Classification correction REST endpoints (ADR 0059).
//
//   POST /api/v1/documents/{id}/classify/correct
//   GET  /api/v1/documents/{id}/classify/corrections
//   POST /api/v1/admin/documents/bulk-reclassify
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/repository"
	"github.com/vaultdms/vaultdms/services/document/internal/service"
)

type ClassifyCorrectionHandler struct {
	svc *service.DocumentService
	log zerolog.Logger
}

func NewClassifyCorrectionHandler(svc *service.DocumentService, log zerolog.Logger) *ClassifyCorrectionHandler {
	return &ClassifyCorrectionHandler{svc: svc, log: log}
}

func (h *ClassifyCorrectionHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/documents/{id}/classify/correct", h.correct)
	mux.HandleFunc("GET /api/v1/documents/{id}/classify/corrections", h.list)
	mux.HandleFunc("POST /api/v1/admin/documents/bulk-reclassify", h.bulk)
}

type correctBody struct {
	CorrectedCategory  string   `json:"corrected_category"`
	OriginalCategory   string   `json:"original_category,omitempty"`
	OriginalConfidence *float32 `json:"original_confidence,omitempty"`
	VersionID          string   `json:"version_id,omitempty"`
	Note               string   `json:"note,omitempty"`
}

type bulkBody struct {
	DocumentIDs       []string `json:"document_ids"`
	CorrectedCategory string   `json:"corrected_category"`
	Note              string   `json:"note,omitempty"`
}

type correctionDTO struct {
	ID                 string   `json:"id"`
	DocumentID         string   `json:"document_id"`
	VersionID          string   `json:"version_id"`
	OriginalCategory   string   `json:"original_category"`
	CorrectedCategory  string   `json:"corrected_category"`
	OriginalConfidence *float32 `json:"original_confidence,omitempty"`
	CorrectionSource   string   `json:"correction_source"`
	Note               string   `json:"note,omitempty"`
	CorrectedBy        string   `json:"corrected_by"`
	CreatedAt          string   `json:"created_at"`
}

func (h *ClassifyCorrectionHandler) correct(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	var body correctBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	in := service.CorrectClassificationInput{
		DocumentID:         docID,
		CorrectedCategory:  body.CorrectedCategory,
		OriginalCategory:   body.OriginalCategory,
		OriginalConfidence: body.OriginalConfidence,
		Source:             "manual",
		Note:               body.Note,
	}
	if body.VersionID != "" {
		v, err := uuid.Parse(body.VersionID)
		if err != nil {
			writeErr(w, r, vdmserr.Validation("version_id", "invalid uuid"))
			return
		}
		in.VersionID = &v
	}
	ctx := r.Context()
	c, err := h.svc.CorrectClassification(ctx, in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, correctionToDTO(c))
}

func (h *ClassifyCorrectionHandler) list(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	ctx := r.Context()
	rows, err := h.svc.ListClassificationCorrections(ctx, docID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := make([]correctionDTO, 0, len(rows))
	for _, c := range rows {
		out = append(out, correctionToDTO(&c))
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"corrections": out})
}

func (h *ClassifyCorrectionHandler) bulk(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin", "compliance_officer") {
		return
	}
	var body bulkBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	ids := make([]uuid.UUID, 0, len(body.DocumentIDs))
	for _, s := range body.DocumentIDs {
		id, err := uuid.Parse(s)
		if err != nil {
			writeErr(w, r, vdmserr.Validation("document_ids", "invalid uuid: "+s))
			return
		}
		ids = append(ids, id)
	}
	ctx := r.Context()
	count, err := h.svc.BulkCorrectClassification(ctx, ids, body.CorrectedCategory, body.Note)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"corrected_count": count})
}

func correctionToDTO(c *repository.ClassificationCorrection) correctionDTO {
	return correctionDTO{
		ID:                 c.ID.String(),
		DocumentID:         c.DocumentID.String(),
		VersionID:          c.VersionID.String(),
		OriginalCategory:   c.OriginalCategory,
		CorrectedCategory:  c.CorrectedCategory,
		OriginalConfidence: c.OriginalConfidence,
		CorrectionSource:   c.CorrectionSource,
		Note:               c.Note,
		CorrectedBy:        c.CorrectedBy.String(),
		CreatedAt:          c.CreatedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00"),
	}
}
