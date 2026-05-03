// Compliance PII/PHI REST endpoints (ADR 0054).
//
//   GET  /api/v1/documents/{id}/compliance
//   POST /api/v1/documents/{id}/compliance/{fid}/review
//   GET  /api/v1/admin/compliance/dashboard
//   GET  /api/v1/admin/compliance/findings
//   GET  /api/v1/admin/compliance/config
//   PUT  /api/v1/admin/compliance/config
//   POST /api/v1/admin/compliance/rescan/{id}
//
// Type named ComplianceHandler — no clash with the existing HoldsHandler
// (legal-hold compliance, in compliance_handler.go).
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

type ComplianceHandler struct {
	svc *service.DocumentService
	log zerolog.Logger
}

func NewComplianceHandler(svc *service.DocumentService, log zerolog.Logger) *ComplianceHandler {
	return &ComplianceHandler{svc: svc, log: log}
}

func (h *ComplianceHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/documents/{id}/compliance", h.getForDoc)
	mux.HandleFunc("POST /api/v1/documents/{id}/compliance/{fid}/review", h.review)
	mux.HandleFunc("GET /api/v1/admin/compliance/dashboard", h.dashboard)
	mux.HandleFunc("GET /api/v1/admin/compliance/findings", h.listPending)
	mux.HandleFunc("GET /api/v1/admin/compliance/config", h.getConfig)
	mux.HandleFunc("PUT /api/v1/admin/compliance/config", h.upsertConfig)
	mux.HandleFunc("POST /api/v1/admin/compliance/rescan/{id}", h.rescan)
}

// ---- DTOs ----------------------------------------------------------------

type complianceFindingDTO struct {
	ID                string  `json:"id"`
	DocumentID        string  `json:"document_id"`
	VersionID         string  `json:"version_id"`
	EntityType        string  `json:"entity_type"`
	EntityCategory    string  `json:"entity_category"`
	OccurrenceCount   int32   `json:"occurrence_count"`
	PageNumbers       []int32 `json:"page_numbers"`
	Confidence        float32 `json:"confidence"`
	RiskLevel         string  `json:"risk_level"`
	SampleContext     string  `json:"sample_context"`
	DetectionSource   string  `json:"detection_source"`
	RemediationStatus string  `json:"remediation_status"`
	RemediatedBy      *string `json:"remediated_by,omitempty"`
	RemediatedAt      *string `json:"remediated_at,omitempty"`
	RemediationNote   string  `json:"remediation_note,omitempty"`
	CreatedAt         string  `json:"created_at"`
}

type complianceSummaryDTO struct {
	OverallRisk      string   `json:"overall_risk"`
	PIICount         int32    `json:"pii_count"`
	PHICount         int32    `json:"phi_count"`
	CriticalCount    int32    `json:"critical_count"`
	HighCount        int32    `json:"high_count"`
	MediumCount      int32    `json:"medium_count"`
	LowCount         int32    `json:"low_count"`
	EntityTypesFound []string `json:"entity_types_found"`
	NeedsReview      bool     `json:"needs_review"`
	AutoHeld         bool     `json:"auto_held"`
	ScannedAt        string   `json:"scanned_at"`
}

type complianceConfigDTO struct {
	Enabled                bool            `json:"enabled"`
	AutoHoldOnCritical     bool            `json:"auto_hold_on_critical"`
	NotifyOnHigh           bool            `json:"notify_on_high"`
	NotifyRoles            []string        `json:"notify_roles"`
	PIIEntityRiskOverrides json.RawMessage `json:"pii_entity_risk_overrides"`
	PHIEnabled             bool            `json:"phi_enabled"`
	CustomPatterns         json.RawMessage `json:"custom_patterns"`
}

type reviewBody struct {
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
}

type complianceConfigPatchBody struct {
	Enabled                *bool            `json:"enabled,omitempty"`
	AutoHoldOnCritical     *bool            `json:"auto_hold_on_critical,omitempty"`
	NotifyOnHigh           *bool            `json:"notify_on_high,omitempty"`
	NotifyRoles            *[]string        `json:"notify_roles,omitempty"`
	PIIEntityRiskOverrides *json.RawMessage `json:"pii_entity_risk_overrides,omitempty"`
	PHIEnabled             *bool            `json:"phi_enabled,omitempty"`
	CustomPatterns         *json.RawMessage `json:"custom_patterns,omitempty"`
}

// ---- handlers ------------------------------------------------------------

func (h *ComplianceHandler) getForDoc(w http.ResponseWriter, r *http.Request) {
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
	summary, findings, err := h.svc.GetComplianceForDocument(ctx, docID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	resp := map[string]any{
		"findings": findingsToDTO(findings),
	}
	if summary != nil {
		resp["summary"] = summaryToDTO(summary)
	}
	writeJSONStatus(w, http.StatusOK, resp)
}

func (h *ComplianceHandler) review(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	fid, err := uuid.Parse(r.PathValue("fid"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("fid", "invalid uuid"))
		return
	}
	var body reviewBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	ctx := auth.WithUser(r.Context(), auth.UserInfo{TenantID: tenantID, ID: userID})
	updated, err := h.svc.ReviewComplianceFinding(ctx, docID, fid, body.Status, body.Note)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, findingToDTO(updated))
}

func (h *ComplianceHandler) dashboard(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin", "compliance_officer") {
		return
	}
	ctx := auth.WithUser(r.Context(), auth.UserInfo{TenantID: tenantID, ID: userID})
	d, err := h.svc.ComplianceDashboard(ctx)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	type entityFreq struct {
		EntityType string `json:"entity_type"`
		Count      int64  `json:"count"`
	}
	top := make([]entityFreq, 0, len(d.TopEntityTypes))
	for _, e := range d.TopEntityTypes {
		top = append(top, entityFreq{e.EntityType, e.Count})
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"total_documents_scanned": d.TotalDocumentsScanned,
		"documents_with_findings": d.DocumentsWithFindings,
		"open_findings":           d.OpenFindings,
		"auto_held_documents":     d.AutoHeldDocuments,
		"risk_distribution":       d.RiskDistribution,
		"top_entity_types":        top,
	})
}

func (h *ComplianceHandler) listPending(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin", "compliance_officer") {
		return
	}
	q := r.URL.Query()
	opts := repository.ListFindingsOpts{
		RiskLevel:  q.Get("risk_level"),
		Status:     q.Get("status"),
		EntityType: q.Get("entity_type"),
		Limit:      int32(parseInt(q.Get("limit"), 50)),
		Offset:     int32(parseInt(q.Get("offset"), 0)),
	}
	ctx := auth.WithUser(r.Context(), auth.UserInfo{TenantID: tenantID, ID: userID})
	rows, total, err := h.svc.ListPendingComplianceFindings(ctx, opts)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"findings": findingsToDTO(rows),
		"total":    total,
		"limit":    opts.Limit,
		"offset":   opts.Offset,
	})
}

func (h *ComplianceHandler) getConfig(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin", "compliance_officer") {
		return
	}
	ctx := auth.WithUser(r.Context(), auth.UserInfo{TenantID: tenantID, ID: userID})
	c, err := h.svc.GetComplianceConfig(ctx)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if c == nil {
		writeJSONStatus(w, http.StatusOK, defaultComplianceConfigDTO())
		return
	}
	writeJSONStatus(w, http.StatusOK, complianceConfigToDTO(c))
}

func (h *ComplianceHandler) upsertConfig(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	var body complianceConfigPatchBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	ctx := auth.WithUser(r.Context(), auth.UserInfo{TenantID: tenantID, ID: userID})
	c, err := h.svc.UpsertComplianceConfig(ctx, repository.ComplianceConfigPatch{
		Enabled:                body.Enabled,
		AutoHoldOnCritical:     body.AutoHoldOnCritical,
		NotifyOnHigh:           body.NotifyOnHigh,
		NotifyRoles:            body.NotifyRoles,
		PIIEntityRiskOverrides: body.PIIEntityRiskOverrides,
		PHIEnabled:             body.PHIEnabled,
		CustomPatterns:         body.CustomPatterns,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, complianceConfigToDTO(c))
}

func (h *ComplianceHandler) rescan(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin", "compliance_officer") {
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	ctx := auth.WithUser(r.Context(), auth.UserInfo{TenantID: tenantID, ID: userID})
	if err := h.svc.RescanCompliance(ctx, docID); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, map[string]any{"status": "queued"})
}

// ---- DTO converters -----------------------------------------------------

func findingsToDTO(in []repository.ComplianceFinding) []complianceFindingDTO {
	out := make([]complianceFindingDTO, 0, len(in))
	for _, f := range in {
		out = append(out, findingToDTO(&f))
	}
	return out
}

func findingToDTO(f *repository.ComplianceFinding) complianceFindingDTO {
	dto := complianceFindingDTO{
		ID:                f.ID.String(),
		DocumentID:        f.DocumentID.String(),
		VersionID:         f.VersionID.String(),
		EntityType:        f.EntityType,
		EntityCategory:    f.EntityCategory,
		OccurrenceCount:   f.OccurrenceCount,
		PageNumbers:       f.PageNumbers,
		Confidence:        f.Confidence,
		RiskLevel:         f.RiskLevel,
		SampleContext:     f.SampleContext,
		DetectionSource:   f.DetectionSource,
		RemediationStatus: f.RemediationStatus,
		RemediationNote:   f.RemediationNote,
		CreatedAt:         f.CreatedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00"),
	}
	if f.RemediatedBy != nil {
		v := f.RemediatedBy.String()
		dto.RemediatedBy = &v
	}
	if f.RemediatedAt != nil {
		v := f.RemediatedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00")
		dto.RemediatedAt = &v
	}
	return dto
}

func summaryToDTO(s *repository.ComplianceSummary) complianceSummaryDTO {
	return complianceSummaryDTO{
		OverallRisk:      s.OverallRisk,
		PIICount:         s.PIICount,
		PHICount:         s.PHICount,
		CriticalCount:    s.CriticalCount,
		HighCount:        s.HighCount,
		MediumCount:      s.MediumCount,
		LowCount:         s.LowCount,
		EntityTypesFound: s.EntityTypesFound,
		NeedsReview:      s.NeedsReview,
		AutoHeld:         s.AutoHeld,
		ScannedAt:        s.ScannedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00"),
	}
}

func complianceConfigToDTO(c *repository.ComplianceConfig) complianceConfigDTO {
	return complianceConfigDTO{
		Enabled:                c.Enabled,
		AutoHoldOnCritical:     c.AutoHoldOnCritical,
		NotifyOnHigh:           c.NotifyOnHigh,
		NotifyRoles:            c.NotifyRoles,
		PIIEntityRiskOverrides: c.PIIEntityRiskOverrides,
		PHIEnabled:             c.PHIEnabled,
		CustomPatterns:         c.CustomPatterns,
	}
}

func defaultComplianceConfigDTO() complianceConfigDTO {
	return complianceConfigDTO{
		Enabled:                true,
		AutoHoldOnCritical:     false,
		NotifyOnHigh:           true,
		NotifyRoles:            []string{"compliance_officer", "admin"},
		PIIEntityRiskOverrides: json.RawMessage(`{}`),
		PHIEnabled:             false,
		CustomPatterns:         json.RawMessage(`[]`),
	}
}
