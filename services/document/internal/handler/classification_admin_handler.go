// Classification-based access control admin API (§8).
//
//	GET    /api/v1/admin/classification/config
//	PUT    /api/v1/admin/classification/config
//	GET    /api/v1/admin/classification/rules
//	POST   /api/v1/admin/classification/rules
//	DELETE /api/v1/admin/classification/rules/{id}
//	PUT    /api/v1/admin/classification/users/{userID}/clearance
//	PUT    /api/v1/admin/documents/{id}/classification   (manual override)
//
// Mounted behind SessionAuth; each handler re-checks owner/admin.
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

type ClassificationAdminHandler struct {
	svc *service.DocumentService
	log zerolog.Logger
}

func NewClassificationAdminHandler(svc *service.DocumentService, log zerolog.Logger) *ClassificationAdminHandler {
	return &ClassificationAdminHandler{svc: svc, log: log}
}

func (h *ClassificationAdminHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/classification/config", h.getConfig)
	mux.HandleFunc("PUT /api/v1/admin/classification/config", h.putConfig)
	mux.HandleFunc("GET /api/v1/admin/classification/rules", h.listRules)
	mux.HandleFunc("POST /api/v1/admin/classification/rules", h.createRule)
	mux.HandleFunc("DELETE /api/v1/admin/classification/rules/{id}", h.deleteRule)
	mux.HandleFunc("PUT /api/v1/admin/classification/users/{userID}/clearance", h.setClearance)
	mux.HandleFunc("PUT /api/v1/admin/documents/{id}/classification", h.setDocClassification)
}

// ---- DTOs ---------------------------------------------------------------

type classConfigDTO struct {
	Enabled               bool `json:"enabled"`
	PHIRequiresRestricted bool `json:"phi_requires_restricted"`
}

type classRuleDTO struct {
	ID                string `json:"id"`
	MinClassification string `json:"min_classification"`
	Action            string `json:"action"`
	RequiredClearance string `json:"required_clearance"`
	AppliesToPHI      bool   `json:"applies_to_phi"`
	Description       string `json:"description"`
	CreatedAt         string `json:"created_at"`
}

func classRuleToDTO(r model.ClassificationRule) classRuleDTO {
	return classRuleDTO{
		ID:                r.ID.String(),
		MinClassification: r.MinClassification,
		Action:            r.Action,
		RequiredClearance: r.RequiredClearance,
		AppliesToPHI:      r.AppliesToPHI,
		Description:       r.Description,
		CreatedAt:         r.CreatedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00"),
	}
}

// ---- handlers -----------------------------------------------------------

func (h *ClassificationAdminHandler) getConfig(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	cfg, err := h.svc.GetClassificationConfig(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, classConfigDTO{Enabled: cfg.Enabled, PHIRequiresRestricted: cfg.PHIRequiresRestricted})
}

func (h *ClassificationAdminHandler) putConfig(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	var body classConfigDTO
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	cfg, err := h.svc.UpsertClassificationConfig(r.Context(), body.Enabled, body.PHIRequiresRestricted)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, classConfigDTO{Enabled: cfg.Enabled, PHIRequiresRestricted: cfg.PHIRequiresRestricted})
}

func (h *ClassificationAdminHandler) listRules(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	rules, err := h.svc.ListClassificationRules(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := make([]classRuleDTO, 0, len(rules))
	for _, rl := range rules {
		out = append(out, classRuleToDTO(rl))
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"rules": out})
}

func (h *ClassificationAdminHandler) createRule(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	var body classRuleDTO
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	created, err := h.svc.CreateClassificationRule(r.Context(), model.ClassificationRule{
		MinClassification: body.MinClassification,
		Action:            body.Action,
		RequiredClearance: body.RequiredClearance,
		AppliesToPHI:      body.AppliesToPHI,
		Description:       body.Description,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, classRuleToDTO(created))
}

func (h *ClassificationAdminHandler) deleteRule(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
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
	if err := h.svc.DeleteClassificationRule(r.Context(), id); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"status": "deleted"})
}

type clearanceBody struct {
	Clearance string `json:"clearance"`
}

func (h *ClassificationAdminHandler) setClearance(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	userID, err := uuid.Parse(r.PathValue("userID"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("userID", "invalid uuid"))
		return
	}
	var body clearanceBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	if err := h.svc.SetUserClearance(r.Context(), userID, body.Clearance); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"status": "updated", "clearance": body.Clearance})
}

type docClassBody struct {
	SecurityClassification string `json:"security_classification"`
}

func (h *ClassificationAdminHandler) setDocClassification(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	var body docClassBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	if err := h.svc.SetDocumentClassification(r.Context(), docID, body.SecurityClassification); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"status": "updated", "security_classification": body.SecurityClassification})
}
