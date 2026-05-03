// Anomaly REST endpoints (ADR 0058) — read + review only.
// The "run" trigger lives on the intelligence service.
//
//   GET  /api/v1/admin/anomalies
//   GET  /api/v1/admin/anomalies/{id}
//   POST /api/v1/admin/anomalies/findings/{fid}/resolve
//   GET  /api/v1/admin/anomaly-config
//   PUT  /api/v1/admin/anomaly-config
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/auth"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/repository"
	"github.com/vaultdms/vaultdms/services/document/internal/service"
)

type AnomalyHandler struct {
	svc *service.DocumentService
	log zerolog.Logger
}

func NewAnomalyHandler(svc *service.DocumentService, log zerolog.Logger) *AnomalyHandler {
	return &AnomalyHandler{svc: svc, log: log}
}

func (h *AnomalyHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/anomalies", h.listReports)
	mux.HandleFunc("GET /api/v1/admin/anomalies/{id}", h.getReport)
	mux.HandleFunc("POST /api/v1/admin/anomalies/findings/{fid}/resolve", h.resolveFinding)
	mux.HandleFunc("GET /api/v1/admin/anomaly-config", h.getConfig)
	mux.HandleFunc("PUT /api/v1/admin/anomaly-config", h.upsertConfig)
}

// ---- DTOs ---------------------------------------------------------------

type anomalyReportDTO struct {
	ID              string          `json:"id"`
	WorkspaceID     *string         `json:"workspace_id,omitempty"`
	AnalysisType    string          `json:"analysis_type"`
	Status          string          `json:"status"`
	TotalDocuments  int32           `json:"total_documents"`
	AnomaliesFound  int32           `json:"anomalies_found"`
	Summary         json.RawMessage `json:"summary"`
	ErrorMessage    string          `json:"error_message,omitempty"`
	TriggeredBy     string          `json:"triggered_by"`
	RequestedBy     *string         `json:"requested_by,omitempty"`
	CompletedAt     *string         `json:"completed_at,omitempty"`
	CreatedAt       string          `json:"created_at"`
}

type anomalyFindingDTO struct {
	ID              string          `json:"id"`
	ReportID        string          `json:"report_id"`
	DocumentID      string          `json:"document_id"`
	AnomalyType     string          `json:"anomaly_type"`
	Severity        string          `json:"severity"`
	Description     string          `json:"description"`
	Evidence        json.RawMessage `json:"evidence"`
	ZScore          *float32        `json:"z_score,omitempty"`
	SimilarityScore *float32        `json:"similarity_score,omitempty"`
	Status          string          `json:"status"`
	ResolvedBy      *string         `json:"resolved_by,omitempty"`
	ResolvedAt      *string         `json:"resolved_at,omitempty"`
	ResolutionNote  string          `json:"resolution_note,omitempty"`
	CreatedAt       string          `json:"created_at"`
}

type anomalyConfigDTO struct {
	Enabled                  bool    `json:"enabled"`
	ScheduleCron             string  `json:"schedule_cron"`
	ZScoreThreshold          float32 `json:"z_score_threshold"`
	ContentDistanceThreshold float32 `json:"content_distance_threshold"`
	MinDocumentsForAnalysis  int32   `json:"min_documents_for_analysis"`
	AnalyzeMetadata          bool    `json:"analyze_metadata"`
	AnalyzeContent           bool    `json:"analyze_content"`
	AnalyzeBehavioral        bool    `json:"analyze_behavioral"`
}

type resolveBody struct {
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
}

type anomalyConfigPatchBody struct {
	Enabled                  *bool    `json:"enabled,omitempty"`
	ScheduleCron             *string  `json:"schedule_cron,omitempty"`
	ZScoreThreshold          *float32 `json:"z_score_threshold,omitempty"`
	ContentDistanceThreshold *float32 `json:"content_distance_threshold,omitempty"`
	MinDocumentsForAnalysis  *int32   `json:"min_documents_for_analysis,omitempty"`
	AnalyzeMetadata          *bool    `json:"analyze_metadata,omitempty"`
	AnalyzeContent           *bool    `json:"analyze_content,omitempty"`
	AnalyzeBehavioral        *bool    `json:"analyze_behavioral,omitempty"`
}

// ---- handlers ------------------------------------------------------------

func (h *AnomalyHandler) listReports(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin", "compliance_officer") {
		return
	}
	q := r.URL.Query()
	opts := repository.ListReportsOpts{
		Status: q.Get("status"),
		Limit:  int32(parseInt(q.Get("limit"), 50)),
		Offset: int32(parseInt(q.Get("offset"), 0)),
	}
	if w2 := q.Get("workspace_id"); w2 != "" {
		if parsed, err := uuid.Parse(w2); err == nil {
			opts.WorkspaceID = &parsed
		}
	}
	ctx := auth.WithUser(r.Context(), auth.UserInfo{TenantID: tenantID, ID: userID})
	rows, total, err := h.svc.ListAnomalyReports(ctx, opts)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := make([]anomalyReportDTO, 0, len(rows))
	for _, rep := range rows {
		out = append(out, reportToDTO(&rep))
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"reports": out,
		"total":   total,
		"limit":   opts.Limit,
		"offset":  opts.Offset,
	})
}

func (h *AnomalyHandler) getReport(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin", "compliance_officer") {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	ctx := auth.WithUser(r.Context(), auth.UserInfo{TenantID: tenantID, ID: userID})
	report, findings, err := h.svc.GetAnomalyReport(ctx, id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"report":   reportToDTO(report),
		"findings": findingsToAnomalyDTO(findings),
	})
}

func (h *AnomalyHandler) resolveFinding(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin", "compliance_officer") {
		return
	}
	fid, err := uuid.Parse(r.PathValue("fid"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("fid", "invalid uuid"))
		return
	}
	var body resolveBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	ctx := auth.WithUser(r.Context(), auth.UserInfo{TenantID: tenantID, ID: userID})
	updated, err := h.svc.ResolveAnomalyFinding(ctx, fid, body.Status, body.Note)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, findingToAnomalyDTO(updated))
}

func (h *AnomalyHandler) getConfig(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin", "compliance_officer") {
		return
	}
	ctx := auth.WithUser(r.Context(), auth.UserInfo{TenantID: tenantID, ID: userID})
	c, err := h.svc.GetAnomalyConfig(ctx)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if c == nil {
		writeJSONStatus(w, http.StatusOK, defaultAnomalyConfigDTO())
		return
	}
	writeJSONStatus(w, http.StatusOK, anomalyConfigToDTO(c))
}

func (h *AnomalyHandler) upsertConfig(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	var body anomalyConfigPatchBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	ctx := auth.WithUser(r.Context(), auth.UserInfo{TenantID: tenantID, ID: userID})
	c, err := h.svc.UpsertAnomalyConfig(ctx, repository.AnomalyConfigPatch{
		Enabled:                  body.Enabled,
		ScheduleCron:             body.ScheduleCron,
		ZScoreThreshold:          body.ZScoreThreshold,
		ContentDistanceThreshold: body.ContentDistanceThreshold,
		MinDocumentsForAnalysis:  body.MinDocumentsForAnalysis,
		AnalyzeMetadata:          body.AnalyzeMetadata,
		AnalyzeContent:           body.AnalyzeContent,
		AnalyzeBehavioral:        body.AnalyzeBehavioral,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, anomalyConfigToDTO(c))
}

// ---- DTO converters -----------------------------------------------------

func reportToDTO(rep *repository.AnomalyReport) anomalyReportDTO {
	dto := anomalyReportDTO{
		ID:             rep.ID.String(),
		AnalysisType:   rep.AnalysisType,
		Status:         rep.Status,
		TotalDocuments: rep.TotalDocuments,
		AnomaliesFound: rep.AnomaliesFound,
		Summary:        rep.Summary,
		ErrorMessage:   rep.ErrorMessage,
		TriggeredBy:    rep.TriggeredBy,
		CreatedAt:      rep.CreatedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00"),
	}
	if rep.WorkspaceID != nil {
		v := rep.WorkspaceID.String()
		dto.WorkspaceID = &v
	}
	if rep.RequestedBy != nil {
		v := rep.RequestedBy.String()
		dto.RequestedBy = &v
	}
	if rep.CompletedAt != nil {
		v := rep.CompletedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00")
		dto.CompletedAt = &v
	}
	return dto
}

func findingsToAnomalyDTO(in []repository.AnomalyFinding) []anomalyFindingDTO {
	out := make([]anomalyFindingDTO, 0, len(in))
	for _, f := range in {
		out = append(out, findingToAnomalyDTO(&f))
	}
	return out
}

func findingToAnomalyDTO(f *repository.AnomalyFinding) anomalyFindingDTO {
	dto := anomalyFindingDTO{
		ID:              f.ID.String(),
		ReportID:        f.ReportID.String(),
		DocumentID:      f.DocumentID.String(),
		AnomalyType:     f.AnomalyType,
		Severity:        f.Severity,
		Description:     f.Description,
		Evidence:        f.Evidence,
		ZScore:          f.ZScore,
		SimilarityScore: f.SimilarityScore,
		Status:          f.Status,
		ResolutionNote:  f.ResolutionNote,
		CreatedAt:       f.CreatedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00"),
	}
	if f.ResolvedBy != nil {
		v := f.ResolvedBy.String()
		dto.ResolvedBy = &v
	}
	if f.ResolvedAt != nil {
		v := f.ResolvedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00")
		dto.ResolvedAt = &v
	}
	return dto
}

func anomalyConfigToDTO(c *repository.AnomalyConfig) anomalyConfigDTO {
	return anomalyConfigDTO{
		Enabled:                  c.Enabled,
		ScheduleCron:             c.ScheduleCron,
		ZScoreThreshold:          c.ZScoreThreshold,
		ContentDistanceThreshold: c.ContentDistanceThreshold,
		MinDocumentsForAnalysis:  c.MinDocumentsForAnalysis,
		AnalyzeMetadata:          c.AnalyzeMetadata,
		AnalyzeContent:           c.AnalyzeContent,
		AnalyzeBehavioral:        c.AnalyzeBehavioral,
	}
}

func defaultAnomalyConfigDTO() anomalyConfigDTO {
	return anomalyConfigDTO{
		Enabled:                  true,
		ScheduleCron:             "0 2 * * 0",
		ZScoreThreshold:          2.5,
		ContentDistanceThreshold: 0.7,
		MinDocumentsForAnalysis:  20,
		AnalyzeMetadata:          true,
		AnalyzeContent:           true,
		AnalyzeBehavioral:        true,
	}
}
