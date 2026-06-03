// Auto-tag REST endpoints (ADR 0052).
//
//   GET  /api/v1/documents/{id}/tag-suggestions
//   POST /api/v1/documents/{id}/tag-suggestions/review
//   GET  /api/v1/admin/tag-suggestions
//   GET  /api/v1/admin/auto-tag-config
//   PUT  /api/v1/admin/auto-tag-config
package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"
)

type AutoTagHandler struct {
	svc *service.DocumentService
	log zerolog.Logger
}

func NewAutoTagHandler(svc *service.DocumentService, log zerolog.Logger) *AutoTagHandler {
	return &AutoTagHandler{svc: svc, log: log}
}

func (h *AutoTagHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/documents/{id}/tag-suggestions", h.listForDocument)
	mux.HandleFunc("POST /api/v1/documents/{id}/tag-suggestions/review", h.batchReview)
	mux.HandleFunc("GET /api/v1/admin/tag-suggestions", h.listPending)
	mux.HandleFunc("GET /api/v1/admin/auto-tag-config", h.getConfig)
	mux.HandleFunc("PUT /api/v1/admin/auto-tag-config", h.upsertConfig)
}

// ---- response shapes -----------------------------------------------------

type tagSuggestionDTO struct {
	ID                  string          `json:"id"`
	DocumentID          string          `json:"document_id"`
	VersionID           string          `json:"version_id"`
	TagName             string          `json:"tag_name"`
	Source              string          `json:"source"`
	SourceDetail        json.RawMessage `json:"source_detail"`
	Confidence          float32         `json:"confidence"`
	Status              string          `json:"status"`
	ReviewedBy          *string         `json:"reviewed_by,omitempty"`
	ReviewedAt          *string         `json:"reviewed_at,omitempty"`
	CreatedAt           string          `json:"created_at"`
}

type autoTagConfigDTO struct {
	Enabled              bool            `json:"enabled"`
	AutoApplyThreshold   float32         `json:"auto_apply_threshold"`
	SuggestThreshold     float32         `json:"suggest_threshold"`
	MaxTagsPerDocument   int32           `json:"max_tags_per_document"`
	BlockedTags          []string        `json:"blocked_tags"`
	SourceWeights        json.RawMessage `json:"source_weights"`
}

type batchReviewBody struct {
	Actions []struct {
		SuggestionID string `json:"suggestion_id"`
		Action       string `json:"action"`
	} `json:"actions"`
}

type batchReviewResult struct {
	AcceptedCount int      `json:"accepted_count"`
	RejectedCount int      `json:"rejected_count"`
	AcceptedTags  []string `json:"accepted_tags"`
	RejectedTags  []string `json:"rejected_tags"`
}

type configPatchBody struct {
	Enabled              *bool            `json:"enabled,omitempty"`
	AutoApplyThreshold   *float32         `json:"auto_apply_threshold,omitempty"`
	SuggestThreshold     *float32         `json:"suggest_threshold,omitempty"`
	MaxTagsPerDocument   *int32           `json:"max_tags_per_document,omitempty"`
	BlockedTags          *[]string        `json:"blocked_tags,omitempty"`
	SourceWeights        *json.RawMessage `json:"source_weights,omitempty"`
}

// ---- handlers ------------------------------------------------------------

func (h *AutoTagHandler) listForDocument(w http.ResponseWriter, r *http.Request) {
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
	rows, conf, err := h.svc.ListTagSuggestions(ctx, docID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	resp := map[string]any{
		"suggestions": suggestionsToDTO(rows),
	}
	if conf != nil {
		resp["config"] = configToDTO(conf)
	}
	writeJSONStatus(w, http.StatusOK, resp)
}

func (h *AutoTagHandler) batchReview(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	var body batchReviewBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	actions := make([]repository.ReviewAction, 0, len(body.Actions))
	for _, a := range body.Actions {
		sid, perr := uuid.Parse(a.SuggestionID)
		if perr != nil {
			writeErr(w, r, vdmserr.Validation("suggestion_id", "invalid uuid"))
			return
		}
		actions = append(actions, repository.ReviewAction{
			SuggestionID: sid, Action: a.Action,
		})
	}

	ctx := r.Context()
	summary, err := h.svc.BatchReviewTagSuggestions(ctx, docID, actions)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	res := batchReviewResult{
		AcceptedCount: len(summary.Accepted),
		RejectedCount: len(summary.Rejected),
		AcceptedTags:  tagNames(summary.Accepted),
		RejectedTags:  tagNames(summary.Rejected),
	}
	writeJSONStatus(w, http.StatusOK, res)
}

func (h *AutoTagHandler) listPending(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin", "compliance_officer") {
		return
	}
	q := r.URL.Query()
	opts := repository.ListPendingOpts{
		MinConfidence: parseFloat32(q.Get("min_confidence"), 0),
		MaxConfidence: parseFloat32(q.Get("max_confidence"), 1),
		Limit:         int32(parseInt(q.Get("limit"), 50)),
		Offset:        int32(parseInt(q.Get("offset"), 0)),
	}
	ctx := r.Context()
	rows, total, err := h.svc.ListPendingTagSuggestions(ctx, opts)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"suggestions": suggestionsToDTO(rows),
		"total":       total,
		"limit":       opts.Limit,
		"offset":      opts.Offset,
	})
}

func (h *AutoTagHandler) getConfig(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin", "compliance_officer") {
		return
	}
	ctx := r.Context()
	c, err := h.svc.GetAutoTagConfig(ctx)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if c == nil {
		writeJSONStatus(w, http.StatusOK, defaultConfigDTO())
		return
	}
	writeJSONStatus(w, http.StatusOK, configToDTO(c))
}

func (h *AutoTagHandler) upsertConfig(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	var body configPatchBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	patch := repository.AutoTagConfigPatch{
		Enabled:            body.Enabled,
		AutoApplyThreshold: body.AutoApplyThreshold,
		SuggestThreshold:   body.SuggestThreshold,
		MaxTagsPerDocument: body.MaxTagsPerDocument,
		BlockedTags:        body.BlockedTags,
		SourceWeights:      body.SourceWeights,
	}
	ctx := r.Context()
	c, err := h.svc.UpsertAutoTagConfig(ctx, patch)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, configToDTO(c))
}

// ---- conversion helpers --------------------------------------------------

func suggestionsToDTO(in []repository.TagSuggestion) []tagSuggestionDTO {
	out := make([]tagSuggestionDTO, 0, len(in))
	for _, s := range in {
		dto := tagSuggestionDTO{
			ID:           s.ID.String(),
			DocumentID:   s.DocumentID.String(),
			VersionID:    s.VersionID.String(),
			TagName:      s.TagName,
			Source:       s.Source,
			SourceDetail: s.SourceDetail,
			Confidence:   s.Confidence,
			Status:       s.Status,
			CreatedAt:    s.CreatedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00"),
		}
		if s.ReviewedBy != nil {
			v := s.ReviewedBy.String()
			dto.ReviewedBy = &v
		}
		if s.ReviewedAt != nil {
			v := s.ReviewedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00")
			dto.ReviewedAt = &v
		}
		out = append(out, dto)
	}
	return out
}

func configToDTO(c *repository.AutoTagConfig) autoTagConfigDTO {
	return autoTagConfigDTO{
		Enabled:            c.Enabled,
		AutoApplyThreshold: c.AutoApplyThreshold,
		SuggestThreshold:   c.SuggestThreshold,
		MaxTagsPerDocument: c.MaxTagsPerDocument,
		BlockedTags:        c.BlockedTags,
		SourceWeights:      c.SourceWeights,
	}
}

func defaultConfigDTO() autoTagConfigDTO {
	return autoTagConfigDTO{
		Enabled:            true,
		AutoApplyThreshold: 0.95,
		SuggestThreshold:   0.60,
		MaxTagsPerDocument: 20,
		BlockedTags:        []string{},
		SourceWeights:      json.RawMessage(`{"ner":1.0,"classification":0.8,"llm":0.9,"pattern":0.7}`),
	}
}

func tagNames(in []repository.TagSuggestion) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, s.TagName)
	}
	return out
}

func parseFloat32(s string, def float32) float32 {
	if s == "" {
		return def
	}
	v, err := strconv.ParseFloat(s, 32)
	if err != nil {
		return def
	}
	return float32(v)
}

func parseInt(s string, def int) int {
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}
