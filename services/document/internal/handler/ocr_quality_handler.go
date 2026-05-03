// OCR quality REST endpoints (ADR 0057).
//
//   GET  /api/v1/documents/{id}/ocr-quality
//   POST /api/v1/documents/{id}/ocr-quality/{vid}/{page}/review
//   GET  /api/v1/admin/ocr-quality/review-queue
//   GET  /api/v1/admin/ocr-quality/stats
//   GET  /api/v1/admin/ocr-quality/config
//   PUT  /api/v1/admin/ocr-quality/config
package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/auth"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/repository"
	"github.com/vaultdms/vaultdms/services/document/internal/service"
)

type OCRQualityHandler struct {
	svc *service.DocumentService
	log zerolog.Logger
}

func NewOCRQualityHandler(svc *service.DocumentService, log zerolog.Logger) *OCRQualityHandler {
	return &OCRQualityHandler{svc: svc, log: log}
}

func (h *OCRQualityHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/documents/{id}/ocr-quality", h.getForDoc)
	mux.HandleFunc("POST /api/v1/documents/{id}/ocr-quality/{vid}/{page}/review", h.review)
	mux.HandleFunc("GET /api/v1/admin/ocr-quality/review-queue", h.queue)
	mux.HandleFunc("GET /api/v1/admin/ocr-quality/stats", h.stats)
	mux.HandleFunc("GET /api/v1/admin/ocr-quality/config", h.getConfig)
	mux.HandleFunc("PUT /api/v1/admin/ocr-quality/config", h.upsertConfig)
}

// ---- DTOs ---------------------------------------------------------------

type ocrPageDTO struct {
	ID              string   `json:"id"`
	PageNumber      int32    `json:"page_number"`
	OverallScore    float32  `json:"overall_score"`
	CharConfidence  *float32 `json:"char_confidence,omitempty"`
	WordDensity     *float32 `json:"word_density,omitempty"`
	LineRegularity  *float32 `json:"line_regularity,omitempty"`
	NoiseRatio      *float32 `json:"noise_ratio,omitempty"`
	LanguageScore   *float32 `json:"language_score,omitempty"`
	Issues          []string `json:"issues"`
	WordCount       int32    `json:"word_count"`
	CharCount       int32    `json:"char_count"`
	NeedsReview     bool     `json:"needs_review"`
	Reviewed        bool     `json:"reviewed"`
	ReviewedBy      *string  `json:"reviewed_by,omitempty"`
	ReviewedAt      *string  `json:"reviewed_at,omitempty"`
	ReviewNote      string   `json:"review_note,omitempty"`
	CreatedAt       string   `json:"created_at"`
}

type ocrSummaryDTO struct {
	AvgScore           float32 `json:"avg_score"`
	MinScore           float32 `json:"min_score"`
	MaxScore           float32 `json:"max_score"`
	TotalPages         int32   `json:"total_pages"`
	PagesNeedingReview int32   `json:"pages_needing_review"`
	QualityGrade       string  `json:"quality_grade"`
	AutoRetried        bool    `json:"auto_retried"`
	VersionID          string  `json:"version_id"`
	ScoredAt           string  `json:"scored_at"`
}

type queueItemDTO struct {
	DocumentID         string  `json:"document_id"`
	VersionID          string  `json:"version_id"`
	AvgScore           float32 `json:"avg_score"`
	QualityGrade       string  `json:"quality_grade"`
	PagesNeedingReview int32   `json:"pages_needing_review"`
	TotalPages         int32   `json:"total_pages"`
	ScoredAt           string  `json:"scored_at"`
}

type ocrConfigDTO struct {
	Enabled             bool    `json:"enabled"`
	ReviewThreshold     float32 `json:"review_threshold"`
	ExcellentThreshold  float32 `json:"excellent_threshold"`
	GoodThreshold       float32 `json:"good_threshold"`
	FairThreshold       float32 `json:"fair_threshold"`
	AutoRetryBelow      float32 `json:"auto_retry_below"`
	NotifyOnPoor        bool    `json:"notify_on_poor"`
}

type reviewBodyOCR struct {
	Note string `json:"note,omitempty"`
}

type configPatchOCRBody struct {
	Enabled             *bool    `json:"enabled,omitempty"`
	ReviewThreshold     *float32 `json:"review_threshold,omitempty"`
	ExcellentThreshold  *float32 `json:"excellent_threshold,omitempty"`
	GoodThreshold       *float32 `json:"good_threshold,omitempty"`
	FairThreshold       *float32 `json:"fair_threshold,omitempty"`
	AutoRetryBelow      *float32 `json:"auto_retry_below,omitempty"`
	NotifyOnPoor        *bool    `json:"notify_on_poor,omitempty"`
}

// ---- handlers ------------------------------------------------------------

func (h *OCRQualityHandler) getForDoc(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	ctx := auth.WithUser(r.Context(), auth.UserInfo{TenantID: tenantID, ID: userID})
	summary, scores, err := h.svc.GetOCRQualityForDocument(ctx, docID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	resp := map[string]any{"pages": pagesToDTO(scores)}
	if summary != nil {
		resp["summary"] = summaryToOCRDTO(summary)
	}
	writeJSONStatus(w, http.StatusOK, resp)
}

func (h *OCRQualityHandler) review(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	vid, err := uuid.Parse(r.PathValue("vid"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("vid", "invalid uuid"))
		return
	}
	page, err := strconv.Atoi(r.PathValue("page"))
	if err != nil || page <= 0 {
		writeErr(w, r, vdmserr.Validation("page", "must be a positive integer"))
		return
	}
	var body reviewBodyOCR
	if r.ContentLength > 0 {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	ctx := auth.WithUser(r.Context(), auth.UserInfo{TenantID: tenantID, ID: userID})
	updated, err := h.svc.ReviewOCRQualityPage(ctx, docID, vid, int32(page), body.Note)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, pageToDTO(updated))
}

func (h *OCRQualityHandler) queue(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin", "compliance_officer") {
		return
	}
	q := r.URL.Query()
	opts := repository.ListReviewQueueOpts{
		GradeFilter: q.Get("grade"),
		Limit:       int32(parseInt(q.Get("limit"), 50)),
		Offset:      int32(parseInt(q.Get("offset"), 0)),
	}
	ctx := auth.WithUser(r.Context(), auth.UserInfo{TenantID: tenantID, ID: userID})
	rows, total, err := h.svc.ListOCRQualityReviewQueue(ctx, opts)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := make([]queueItemDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, queueItemDTO{
			DocumentID:         r.DocumentID.String(),
			VersionID:          r.VersionID.String(),
			AvgScore:           r.AvgScore,
			QualityGrade:       r.QualityGrade,
			PagesNeedingReview: r.PagesNeedingReview,
			TotalPages:         r.TotalPages,
			ScoredAt:           r.ScoredAt.UTC().Format("2006-01-02T15:04:05.999Z07:00"),
		})
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"items":  out,
		"total":  total,
		"limit":  opts.Limit,
		"offset": opts.Offset,
	})
}

func (h *OCRQualityHandler) stats(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin", "compliance_officer") {
		return
	}
	ctx := auth.WithUser(r.Context(), auth.UserInfo{TenantID: tenantID, ID: userID})
	st, err := h.svc.OCRQualityStats(ctx)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"total_documents":        st.TotalDocuments,
		"documents_by_grade":     st.DocumentsByGrade,
		"open_review_pages":      st.OpenReviewPages,
		"auto_retried_documents": st.AutoRetriedDocuments,
	})
}

func (h *OCRQualityHandler) getConfig(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin", "compliance_officer") {
		return
	}
	ctx := auth.WithUser(r.Context(), auth.UserInfo{TenantID: tenantID, ID: userID})
	c, err := h.svc.GetOCRQualityConfig(ctx)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if c == nil {
		writeJSONStatus(w, http.StatusOK, defaultOCRConfigDTO())
		return
	}
	writeJSONStatus(w, http.StatusOK, ocrConfigToDTO(c))
}

func (h *OCRQualityHandler) upsertConfig(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	var body configPatchOCRBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	ctx := auth.WithUser(r.Context(), auth.UserInfo{TenantID: tenantID, ID: userID})
	c, err := h.svc.UpsertOCRQualityConfig(ctx, repository.OCRQualityConfigPatch{
		Enabled:            body.Enabled,
		ReviewThreshold:    body.ReviewThreshold,
		ExcellentThreshold: body.ExcellentThreshold,
		GoodThreshold:      body.GoodThreshold,
		FairThreshold:      body.FairThreshold,
		AutoRetryBelow:     body.AutoRetryBelow,
		NotifyOnPoor:       body.NotifyOnPoor,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, ocrConfigToDTO(c))
}

// ---- DTO converters -----------------------------------------------------

func pagesToDTO(in []repository.OCRQualityScore) []ocrPageDTO {
	out := make([]ocrPageDTO, 0, len(in))
	for _, p := range in {
		out = append(out, pageToDTO(&p))
	}
	return out
}

func pageToDTO(p *repository.OCRQualityScore) ocrPageDTO {
	dto := ocrPageDTO{
		ID:             p.ID.String(),
		PageNumber:     p.PageNumber,
		OverallScore:   p.OverallScore,
		CharConfidence: p.CharConfidence,
		WordDensity:    p.WordDensity,
		LineRegularity: p.LineRegularity,
		NoiseRatio:     p.NoiseRatio,
		LanguageScore:  p.LanguageScore,
		Issues:         p.Issues,
		WordCount:      p.WordCount,
		CharCount:      p.CharCount,
		NeedsReview:    p.NeedsReview,
		Reviewed:       p.Reviewed,
		ReviewNote:     p.ReviewNote,
		CreatedAt:      p.CreatedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00"),
	}
	if p.ReviewedBy != nil {
		v := p.ReviewedBy.String()
		dto.ReviewedBy = &v
	}
	if p.ReviewedAt != nil {
		v := p.ReviewedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00")
		dto.ReviewedAt = &v
	}
	return dto
}

func summaryToOCRDTO(s *repository.OCRQualitySummary) ocrSummaryDTO {
	return ocrSummaryDTO{
		AvgScore:           s.AvgScore,
		MinScore:           s.MinScore,
		MaxScore:           s.MaxScore,
		TotalPages:         s.TotalPages,
		PagesNeedingReview: s.PagesNeedingReview,
		QualityGrade:       s.QualityGrade,
		AutoRetried:        s.AutoRetried,
		VersionID:          s.VersionID.String(),
		ScoredAt:           s.ScoredAt.UTC().Format("2006-01-02T15:04:05.999Z07:00"),
	}
}

func ocrConfigToDTO(c *repository.OCRQualityConfig) ocrConfigDTO {
	return ocrConfigDTO{
		Enabled:            c.Enabled,
		ReviewThreshold:    c.ReviewThreshold,
		ExcellentThreshold: c.ExcellentThreshold,
		GoodThreshold:      c.GoodThreshold,
		FairThreshold:      c.FairThreshold,
		AutoRetryBelow:     c.AutoRetryBelow,
		NotifyOnPoor:       c.NotifyOnPoor,
	}
}

func defaultOCRConfigDTO() ocrConfigDTO {
	return ocrConfigDTO{
		Enabled:            true,
		ReviewThreshold:    0.60,
		ExcellentThreshold: 0.90,
		GoodThreshold:      0.75,
		FairThreshold:      0.60,
		AutoRetryBelow:     0.40,
		NotifyOnPoor:       false,
	}
}
