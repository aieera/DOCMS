// Redaction review REST endpoints (ADR 0079).
//
//   GET  /api/v1/documents/{id}/redaction-candidates
//   POST /api/v1/documents/{id}/redaction/candidates/{cid}/review
//   POST /api/v1/documents/{id}/redaction/apply
//   GET  /api/v1/documents/{id}/versions/{vid}/unredacted
package handler

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"
)

type RedactionReviewHandler struct {
	svc *service.DocumentService
	log zerolog.Logger
}

func NewRedactionReviewHandler(svc *service.DocumentService, log zerolog.Logger) *RedactionReviewHandler {
	return &RedactionReviewHandler{svc: svc, log: log}
}

func (h *RedactionReviewHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/documents/{id}/redaction-candidates", h.list)
	mux.HandleFunc("POST /api/v1/documents/{id}/redaction/candidates/{cid}/review", h.review)
	mux.HandleFunc("POST /api/v1/documents/{id}/redaction/apply", h.apply)
	mux.HandleFunc("GET /api/v1/documents/{id}/versions/{vid}/unredacted", h.unredacted)
}

// ---- DTOs --------------------------------------------------------------

type redactionCandidateDTO struct {
	ID           string          `json:"id"`
	DocumentID   string          `json:"document_id"`
	VersionID    string          `json:"version_id"`
	Source       string          `json:"source"`
	EntityType   string          `json:"entity_type"`
	EntityValue  string          `json:"entity_value"`
	Rectangles   json.RawMessage `json:"rectangles"`
	PageNumber   *int32          `json:"page_number,omitempty"`
	CharStart    *int32          `json:"char_start,omitempty"`
	CharEnd      *int32          `json:"char_end,omitempty"`
	Status       string          `json:"status"`
	ReviewedBy   *string         `json:"reviewed_by,omitempty"`
	ReviewedAt   *string         `json:"reviewed_at,omitempty"`
	ReviewNote   string          `json:"review_note,omitempty"`
	CreatedAt    string          `json:"created_at"`
}

type listCandidatesResp struct {
	Candidates []redactionCandidateDTO `json:"candidates"`
	Total      int64                   `json:"total"`
	Limit      int32                   `json:"limit"`
	Offset     int32                   `json:"offset"`
}

type redactionReviewBody struct {
	Action string `json:"action"`
	Note   string `json:"note,omitempty"`
}

type applyBody struct {
	VersionID         string `json:"version_id"`
	ForceAdminApprove bool   `json:"force_admin_approve,omitempty"`
}

type applyResp struct {
	JobID          string `json:"job_id"`
	CandidateCount int32  `json:"candidate_count"`
	Status         string `json:"status"`
}

// ---- handlers ----------------------------------------------------------

func (h *RedactionReviewHandler) list(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	q := r.URL.Query()
	opts := repository.ListRedactionCandidatesOpts{
		Status:     q.Get("status"),
		EntityType: q.Get("type"),
		Limit:      int32(parseInt(q.Get("limit"), 200)),
		Offset:     int32(parseInt(q.Get("offset"), 0)),
	}
	if v := q.Get("version_id"); v != "" {
		if vid, vErr := uuid.Parse(v); vErr == nil {
			opts.VersionID = &vid
		}
	}
	rows, total, err := h.svc.ListRedactionCandidates(ctx, docID, opts)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := make([]redactionCandidateDTO, 0, len(rows))
	for _, c := range rows {
		out = append(out, candidateToDTO(&c))
	}
	writeJSONStatus(w, http.StatusOK, listCandidatesResp{
		Candidates: out, Total: total, Limit: opts.Limit, Offset: opts.Offset,
	})
}

func (h *RedactionReviewHandler) review(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	cid, err := uuid.Parse(r.PathValue("cid"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("cid", "invalid uuid"))
		return
	}
	var body redactionReviewBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	c, rErr := h.svc.ReviewRedactionCandidate(ctx, docID, cid, body.Action, body.Note)
	if rErr != nil {
		writeErr(w, r, rErr)
		return
	}
	writeJSONStatus(w, http.StatusOK, candidateToDTO(c))
}

func (h *RedactionReviewHandler) apply(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	// The bulk-apply gate consults the role for the >50 candidate
	// threshold; stamp it on ctx for the service to read. Role is the
	// trusted DB-derived value SessionAuth wrote onto ctx (FIX-1).
	ctx = service.WithCallerRole(ctx, auth.GetUserRole(ctx))
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	var body applyBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	versionID, err := uuid.Parse(body.VersionID)
	if err != nil {
		writeErr(w, r, vdmserr.Validation("version_id", "invalid uuid"))
		return
	}
	res, aErr := h.svc.ApplyRedaction(ctx, docID, service.ApplyRedactionInput{
		VersionID:         versionID,
		ForceAdminApprove: body.ForceAdminApprove,
	})
	if aErr != nil {
		writeErr(w, r, aErr)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, applyResp{
		JobID:          res.JobID.String(),
		CandidateCount: res.CandidateCount,
		Status:         res.Status,
	})
}

// unredacted handles the gated download. The caller must have
// view_unredacted on the document; OPA Rule 6 (owner/admin) still
// passes. Returns a 302 redirect to the storage service's signed-
// download URL for the source version; never the redacted version.
func (h *RedactionReviewHandler) unredacted(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	if err := h.svc.CanViewUnredacted(ctx, docID); err != nil {
		writeErr(w, r, err)
		return
	}
	// {vid} could be either the redacted or the source. Either way,
	// resolve to the SOURCE so the caller never accidentally
	// downloads the redacted blob via this endpoint.
	requestedVid, err := uuid.Parse(r.PathValue("vid"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("vid", "invalid uuid"))
		return
	}
	srcID, lErr := h.svc.LookupSourceVersionFromRedacted(ctx, requestedVid)
	if lErr != nil {
		// Treat "no redaction job found for this version" as a hint
		// that the caller passed the source version directly — that's
		// fine, just download it.
		if vdmserr.KindOf(lErr) != vdmserr.KindNotFound {
			writeErr(w, r, lErr)
			return
		}
		srcID = requestedVid
	}
	// Redirect to the storage service's existing download URL endpoint
	// rather than re-implement signed-URL generation here. The caller
	// just hops one extra HTTP round-trip; the gating happened above.
	target := fmt.Sprintf("/api/v1/documents/%s/versions/%s/download", docID, srcID)
	http.Redirect(w, r, target, http.StatusTemporaryRedirect)
}

// ---- DTO mapper --------------------------------------------------------

func candidateToDTO(c *repository.RedactionCandidate) redactionCandidateDTO {
	dto := redactionCandidateDTO{
		ID:          c.ID.String(),
		DocumentID:  c.DocumentID.String(),
		VersionID:   c.VersionID.String(),
		Source:      c.Source,
		EntityType:  c.EntityType,
		EntityValue: c.EntityValue,
		Rectangles:  c.Rectangles,
		PageNumber:  c.PageNumber,
		CharStart:   c.CharStart,
		CharEnd:     c.CharEnd,
		Status:      c.Status,
		ReviewNote:  c.ReviewNote,
		CreatedAt:   c.CreatedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00"),
	}
	if c.ReviewedBy != nil {
		s := c.ReviewedBy.String()
		dto.ReviewedBy = &s
	}
	if c.ReviewedAt != nil {
		s := c.ReviewedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00")
		dto.ReviewedAt = &s
	}
	return dto
}
