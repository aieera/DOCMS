// Smart-routing REST endpoints (ADR 0053).
//
//   GET    /api/v1/documents/{id}/route-suggestions
//   POST   /api/v1/documents/{id}/route-suggestions/{sid}/accept
//   POST   /api/v1/documents/{id}/route-suggestions/{sid}/dismiss
//   GET    /api/v1/admin/routing-rules
//   POST   /api/v1/admin/routing-rules
//   PUT    /api/v1/admin/routing-rules/{id}
//   DELETE /api/v1/admin/routing-rules/{id}
//   GET    /api/v1/admin/smart-routing-config
//   PUT    /api/v1/admin/smart-routing-config
//   GET    /api/v1/admin/filing-analytics
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"
)

type SmartRouteHandler struct {
	svc *service.DocumentService
	log zerolog.Logger
}

func NewSmartRouteHandler(svc *service.DocumentService, log zerolog.Logger) *SmartRouteHandler {
	return &SmartRouteHandler{svc: svc, log: log}
}

func (h *SmartRouteHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/documents/{id}/route-suggestions", h.list)
	mux.HandleFunc("POST /api/v1/documents/{id}/route-suggestions/{sid}/accept", h.accept)
	mux.HandleFunc("POST /api/v1/documents/{id}/route-suggestions/{sid}/dismiss", h.dismiss)
	mux.HandleFunc("GET /api/v1/admin/routing-rules", h.listRules)
	mux.HandleFunc("POST /api/v1/admin/routing-rules", h.createRule)
	mux.HandleFunc("PUT /api/v1/admin/routing-rules/{id}", h.updateRule)
	mux.HandleFunc("DELETE /api/v1/admin/routing-rules/{id}", h.deleteRule)
	mux.HandleFunc("GET /api/v1/admin/smart-routing-config", h.getConfig)
	mux.HandleFunc("PUT /api/v1/admin/smart-routing-config", h.upsertConfig)
	mux.HandleFunc("GET /api/v1/admin/filing-analytics", h.analytics)
}

// ---- DTOs ---------------------------------------------------------------

type routeSuggestionDTO struct {
	ID                   string          `json:"id"`
	DocumentID           string          `json:"document_id"`
	SuggestedFolderID    string          `json:"suggested_folder_id"`
	SuggestedWorkspaceID *string         `json:"suggested_workspace_id,omitempty"`
	FolderPath           string          `json:"folder_path"`
	MatchSource          string          `json:"match_source"`
	MatchDetail          json.RawMessage `json:"match_detail"`
	Confidence           float32         `json:"confidence"`
	Status               string          `json:"status"`
	CreatedAt            string          `json:"created_at"`
}

type routingRuleDTO struct {
	ID                  string  `json:"id"`
	Name                string  `json:"name"`
	Description         string  `json:"description"`
	CategoryKey         string  `json:"category_key"`
	TargetFolderID      string  `json:"target_folder_id"`
	TargetWorkspaceID   *string `json:"target_workspace_id,omitempty"`
	Priority            int32   `json:"priority"`
	Enabled             bool    `json:"enabled"`
	CreatedBy           string  `json:"created_by"`
	CreatedAt           string  `json:"created_at"`
	UpdatedAt           string  `json:"updated_at"`
}

type ruleCreateBody struct {
	Name              string  `json:"name"`
	Description       string  `json:"description"`
	CategoryKey       string  `json:"category_key"`
	TargetFolderID    string  `json:"target_folder_id"`
	TargetWorkspaceID *string `json:"target_workspace_id,omitempty"`
	Priority          int32   `json:"priority"`
	Enabled           *bool   `json:"enabled,omitempty"`
}

type rulePatchBody struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Priority    *int32  `json:"priority,omitempty"`
	Enabled     *bool   `json:"enabled,omitempty"`
}

type smartRoutingConfigDTO struct {
	Enabled            bool    `json:"enabled"`
	AutoMoveThreshold  float32 `json:"auto_move_threshold"`
	SuggestThreshold   float32 `json:"suggest_threshold"`
	MaxSuggestions     int32   `json:"max_suggestions"`
	LearnFromHistory   bool    `json:"learn_from_history"`
	UseSimilarity      bool    `json:"use_similarity"`
}

type configPatchSrBody struct {
	Enabled           *bool    `json:"enabled,omitempty"`
	AutoMoveThreshold *float32 `json:"auto_move_threshold,omitempty"`
	SuggestThreshold  *float32 `json:"suggest_threshold,omitempty"`
	MaxSuggestions    *int32   `json:"max_suggestions,omitempty"`
	LearnFromHistory  *bool    `json:"learn_from_history,omitempty"`
	UseSimilarity     *bool    `json:"use_similarity,omitempty"`
}

// ---- handlers -----------------------------------------------------------

func (h *SmartRouteHandler) list(w http.ResponseWriter, r *http.Request) {
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
	rows, err := h.svc.ListRouteSuggestions(ctx, docID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"suggestions": suggestionsToRouteDTO(rows),
	})
}

func (h *SmartRouteHandler) accept(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	sid, err := uuid.Parse(r.PathValue("sid"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("sid", "invalid uuid"))
		return
	}
	ctx := r.Context()
	updated, doc, err := h.svc.AcceptRouteSuggestion(ctx, docID, sid)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"suggestion":   routeSuggestionToDTO(updated),
		"document_id":  doc.ID.String(),
		"folder_id":    doc.FolderID.String(),
		"workspace_id": doc.WorkspaceID.String(),
	})
}

func (h *SmartRouteHandler) dismiss(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	sid, err := uuid.Parse(r.PathValue("sid"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("sid", "invalid uuid"))
		return
	}
	ctx := r.Context()
	if err := h.svc.DismissRouteSuggestion(ctx, docID, sid); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusNoContent, nil)
}

func (h *SmartRouteHandler) listRules(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin", "compliance_officer") {
		return
	}
	ctx := r.Context()
	rows, err := h.svc.ListRoutingRules(ctx)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"rules": rulesToDTO(rows)})
}

func (h *SmartRouteHandler) createRule(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	var body ruleCreateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	folderID, err := uuid.Parse(body.TargetFolderID)
	if err != nil {
		writeErr(w, r, vdmserr.Validation("target_folder_id", "invalid uuid"))
		return
	}
	var workspaceID *uuid.UUID
	if body.TargetWorkspaceID != nil && *body.TargetWorkspaceID != "" {
		w2, perr := uuid.Parse(*body.TargetWorkspaceID)
		if perr != nil {
			writeErr(w, r, vdmserr.Validation("target_workspace_id", "invalid uuid"))
			return
		}
		workspaceID = &w2
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	ctx := r.Context()
	rule, err := h.svc.CreateRoutingRule(ctx, repository.RoutingRuleInput{
		Name: body.Name, Description: body.Description,
		CategoryKey: body.CategoryKey, TargetFolderID: folderID,
		TargetWorkspaceID: workspaceID, Priority: body.Priority,
		Enabled: enabled,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, ruleToDTO(rule))
}

func (h *SmartRouteHandler) updateRule(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	var body rulePatchBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	ctx := r.Context()
	rule, err := h.svc.UpdateRoutingRule(ctx, id, repository.RoutingRulePatch{
		Name: body.Name, Description: body.Description,
		Priority: body.Priority, Enabled: body.Enabled,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, ruleToDTO(rule))
}

func (h *SmartRouteHandler) deleteRule(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	ctx := r.Context()
	if err := h.svc.DeleteRoutingRule(ctx, id); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusNoContent, nil)
}

func (h *SmartRouteHandler) getConfig(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin", "compliance_officer") {
		return
	}
	ctx := r.Context()
	c, err := h.svc.GetSmartRoutingConfig(ctx)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if c == nil {
		writeJSONStatus(w, http.StatusOK, defaultSmartRoutingDTO())
		return
	}
	writeJSONStatus(w, http.StatusOK, smartRoutingConfigToDTO(c))
}

func (h *SmartRouteHandler) upsertConfig(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	var body configPatchSrBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	ctx := r.Context()
	c, err := h.svc.UpsertSmartRoutingConfig(ctx, repository.SmartRoutingConfigPatch{
		Enabled:           body.Enabled,
		AutoMoveThreshold: body.AutoMoveThreshold,
		SuggestThreshold:  body.SuggestThreshold,
		MaxSuggestions:    body.MaxSuggestions,
		LearnFromHistory:  body.LearnFromHistory,
		UseSimilarity:     body.UseSimilarity,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, smartRoutingConfigToDTO(c))
}

func (h *SmartRouteHandler) analytics(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin", "compliance_officer") {
		return
	}
	ctx := r.Context()
	a, err := h.svc.FilingAnalytics(ctx)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	type catFolder struct {
		CategoryKey string `json:"category_key"`
		FolderID    string `json:"folder_id"`
		Count       int64  `json:"count"`
	}
	type catFreq struct {
		CategoryKey string `json:"category_key"`
		Count       int64  `json:"count"`
	}
	topFolders := make([]catFolder, 0, len(a.TopFolderByCategory))
	for _, e := range a.TopFolderByCategory {
		topFolders = append(topFolders, catFolder{e.CategoryKey, e.FolderID.String(), e.Count})
	}
	topCats := make([]catFreq, 0, len(a.TopCategories))
	for _, e := range a.TopCategories {
		topCats = append(topCats, catFreq{e.CategoryKey, e.Count})
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"total_filings":           a.TotalFilings,
		"top_categories":          topCats,
		"top_folder_by_category":  topFolders,
		"suggestion_acceptance": map[string]any{
			"total_suggested":  a.SuggestionAcceptance.TotalSuggested,
			"total_accepted":   a.SuggestionAcceptance.TotalAccepted,
			"total_dismissed":  a.SuggestionAcceptance.TotalDismissed,
			"acceptance_rate":  a.SuggestionAcceptance.AcceptanceRate,
		},
	})
}

// ---- DTO converters -----------------------------------------------------

func suggestionsToRouteDTO(in []repository.RouteSuggestion) []routeSuggestionDTO {
	out := make([]routeSuggestionDTO, 0, len(in))
	for _, s := range in {
		out = append(out, routeSuggestionToDTO(&s))
	}
	return out
}

func routeSuggestionToDTO(s *repository.RouteSuggestion) routeSuggestionDTO {
	dto := routeSuggestionDTO{
		ID:                s.ID.String(),
		DocumentID:        s.DocumentID.String(),
		SuggestedFolderID: s.SuggestedFolderID.String(),
		FolderPath:        s.FolderPath,
		MatchSource:       s.MatchSource,
		MatchDetail:       s.MatchDetail,
		Confidence:        s.Confidence,
		Status:            s.Status,
		CreatedAt:         s.CreatedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00"),
	}
	if s.SuggestedWorkspaceID != nil {
		v := s.SuggestedWorkspaceID.String()
		dto.SuggestedWorkspaceID = &v
	}
	return dto
}

func rulesToDTO(in []repository.RoutingRule) []routingRuleDTO {
	out := make([]routingRuleDTO, 0, len(in))
	for _, r := range in {
		out = append(out, ruleToDTO(&r))
	}
	return out
}

func ruleToDTO(r *repository.RoutingRule) routingRuleDTO {
	dto := routingRuleDTO{
		ID:             r.ID.String(),
		Name:           r.Name,
		Description:    r.Description,
		CategoryKey:    r.CategoryKey,
		TargetFolderID: r.TargetFolderID.String(),
		Priority:       r.Priority,
		Enabled:        r.Enabled,
		CreatedBy:      r.CreatedBy.String(),
		CreatedAt:      r.CreatedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00"),
		UpdatedAt:      r.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00"),
	}
	if r.TargetWorkspaceID != nil {
		v := r.TargetWorkspaceID.String()
		dto.TargetWorkspaceID = &v
	}
	return dto
}

func smartRoutingConfigToDTO(c *repository.SmartRoutingConfig) smartRoutingConfigDTO {
	return smartRoutingConfigDTO{
		Enabled:           c.Enabled,
		AutoMoveThreshold: c.AutoMoveThreshold,
		SuggestThreshold:  c.SuggestThreshold,
		MaxSuggestions:    c.MaxSuggestions,
		LearnFromHistory:  c.LearnFromHistory,
		UseSimilarity:     c.UseSimilarity,
	}
}

func defaultSmartRoutingDTO() smartRoutingConfigDTO {
	return smartRoutingConfigDTO{
		Enabled:           true,
		AutoMoveThreshold: 0.98,
		SuggestThreshold:  0.50,
		MaxSuggestions:    5,
		LearnFromHistory:  true,
		UseSimilarity:     true,
	}
}
