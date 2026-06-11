// NER REST endpoints (ADR 0078).
//
//	GET  /api/v1/documents/{id}/entities
//	POST /api/v1/documents/{id}/entities/correct
//	GET  /api/v1/documents/{id}/entities/corrections
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/pkg/license"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"
)

type NERHandler struct {
	svc *service.DocumentService
	log zerolog.Logger
}

func NewNERHandler(svc *service.DocumentService, log zerolog.Logger) *NERHandler {
	return &NERHandler{svc: svc, log: log}
}

func (h *NERHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/documents/{id}/entities", h.list)
	mux.HandleFunc("POST /api/v1/documents/{id}/entities/correct", h.correct)
	mux.HandleFunc("GET /api/v1/documents/{id}/entities/corrections", h.listCorrections)
	mux.HandleFunc("GET /api/v1/admin/ner-config", h.getConfig)
	mux.HandleFunc("PUT /api/v1/admin/ner-config", h.upsertConfig)
	mux.HandleFunc("PUT /api/v1/admin/ner-config/api-key", h.setAPIKey)
	mux.HandleFunc("DELETE /api/v1/admin/ner-config/api-key", h.clearAPIKey)
}

type entityDTO struct {
	ID          string  `json:"id"`
	EntityType  string  `json:"entity_type"`
	EntityValue string  `json:"entity_value"`
	StartOffset int32   `json:"start_offset"`
	EndOffset   int32   `json:"end_offset"`
	Confidence  float32 `json:"confidence"`
	IsPII       bool    `json:"is_pii"`
	Source      string  `json:"source"`
	DetectedAt  string  `json:"detected_at"`
}

type listEntitiesResponse struct {
	Entities []entityDTO `json:"entities"`
	Total    int64       `json:"total"`
	Limit    int32       `json:"limit"`
	Offset   int32       `json:"offset"`
}

type correctEntityBody struct {
	OriginalEntityID string `json:"original_entity_id,omitempty"`
	OriginalType     string `json:"original_type,omitempty"`
	CorrectedType    string `json:"corrected_type"`
	EntityValue      string `json:"entity_value,omitempty"`
	StartOffset      int32  `json:"start_offset,omitempty"`
	EndOffset        int32  `json:"end_offset,omitempty"`
	Action           string `json:"action"`
	Note             string `json:"note,omitempty"`
}

type entityCorrectionDTO struct {
	ID            string `json:"id"`
	DocumentID    string `json:"document_id"`
	VersionID     string `json:"version_id"`
	OriginalType  string `json:"original_type,omitempty"`
	CorrectedType string `json:"corrected_type"`
	EntityValue   string `json:"entity_value"`
	Action        string `json:"action"`
	Note          string `json:"note,omitempty"`
	CorrectedBy   string `json:"corrected_by"`
	CreatedAt     string `json:"created_at"`
}

func (h *NERHandler) list(w http.ResponseWriter, r *http.Request) {
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
	opts := repository.ListEntitiesOpts{
		EntityType: q.Get("type"),
		Source:     q.Get("source"),
		OnlyPII:    q.Get("only_pii") == "true",
		Limit:      int32(parseInt(q.Get("limit"), 200)),
		Offset:     int32(parseInt(q.Get("offset"), 0)),
	}
	if v := q.Get("version_id"); v != "" {
		if vid, vErr := uuid.Parse(v); vErr == nil {
			opts.VersionID = &vid
		}
	}
	rows, total, err := h.svc.ListEntities(ctx, docID, opts)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := make([]entityDTO, 0, len(rows))
	for _, e := range rows {
		out = append(out, entityToDTO(&e))
	}
	writeJSONStatus(w, http.StatusOK, listEntitiesResponse{
		Entities: out,
		Total:    total,
		Limit:    opts.Limit,
		Offset:   opts.Offset,
	})
}

func (h *NERHandler) correct(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	var body correctEntityBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	in := service.CorrectEntityInput{
		OriginalType:  body.OriginalType,
		CorrectedType: body.CorrectedType,
		EntityValue:   body.EntityValue,
		StartOffset:   body.StartOffset,
		EndOffset:     body.EndOffset,
		Action:        body.Action,
		Note:          body.Note,
	}
	if body.OriginalEntityID != "" {
		eid, eErr := uuid.Parse(body.OriginalEntityID)
		if eErr != nil {
			writeErr(w, r, vdmserr.Validation("original_entity_id", "invalid uuid"))
			return
		}
		in.OriginalEntityID = &eid
	}
	c, cErr := h.svc.CorrectEntity(ctx, docID, in)
	if cErr != nil {
		writeErr(w, r, cErr)
		return
	}
	writeJSONStatus(w, http.StatusCreated, entityCorrectionToDTO(c))
}

func (h *NERHandler) listCorrections(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	rows, lErr := h.svc.ListEntityCorrections(ctx, docID)
	if lErr != nil {
		writeErr(w, r, lErr)
		return
	}
	out := make([]entityCorrectionDTO, 0, len(rows))
	for _, c := range rows {
		out = append(out, entityCorrectionToDTO(&c))
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"corrections": out})
}

// ---- NER admin config -----------------------------------------------------

type nerConfigDTO struct {
	Enabled       bool     `json:"llm_enabled"`
	Model         string   `json:"llm_model"`
	EntityTypes   []string `json:"llm_entity_types"`
	BatchSize     int32    `json:"llm_batch_size"`
	MinConfidence float32  `json:"llm_min_confidence"`
	HasAPIKey     bool     `json:"has_api_key"`
	APIKeySetAt   *string  `json:"api_key_set_at,omitempty"`
}

type setAPIKeyBody struct {
	APIKey string `json:"api_key"`
}

type nerConfigBody struct {
	Enabled       *bool     `json:"llm_enabled,omitempty"`
	Model         *string   `json:"llm_model,omitempty"`
	EntityTypes   *[]string `json:"llm_entity_types,omitempty"`
	BatchSize     *int32    `json:"llm_batch_size,omitempty"`
	MinConfidence *float32  `json:"llm_min_confidence,omitempty"`
}

func (h *NERHandler) getConfig(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	c, err := h.svc.GetNERConfig(ctx)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if c == nil {
		// No row yet — return migration-default shape so the UI can
		// render fields without a separate "no config" branch.
		writeJSONStatus(w, http.StatusOK, nerConfigDTO{
			Enabled: false, Model: "claude-haiku-4-5",
			EntityTypes: []string{
				"party_name", "effective_date", "jurisdiction", "governing_law",
				"account_number", "tax_id", "patient_id", "address", "national_id",
			},
			BatchSize: 5, MinConfidence: 0.6,
		})
		return
	}
	writeJSONStatus(w, http.StatusOK, configToNERDTO(c))
}

func (h *NERHandler) upsertConfig(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	var body nerConfigBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	// ADR 0095 — gate *enabling* LLM NER on the intel_llm license feature.
	// Disabling or editing other fields stays open so a tenant that loses
	// the feature can still turn it off. Unlicensed-dev passes (HasFeature
	// returns true on nil claims), so local/CI workflows don't regress.
	if body.Enabled != nil && *body.Enabled && !license.Current().HasFeature("intel_llm") {
		writeErr(w, r, vdmserr.Forbidden("the intel_llm feature is not included in your license"))
		return
	}
	c, err := h.svc.UpsertNERConfig(ctx, repository.NERConfigPatch{
		Enabled:       body.Enabled,
		Model:         body.Model,
		EntityTypes:   body.EntityTypes,
		BatchSize:     body.BatchSize,
		MinConfidence: body.MinConfidence,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, configToNERDTO(c))
}

func (h *NERHandler) setAPIKey(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	var body setAPIKeyBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	if err := h.svc.SetLLMAPIKey(ctx, body.APIKey); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusNoContent, nil)
}

func (h *NERHandler) clearAPIKey(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	if err := h.svc.ClearLLMAPIKey(ctx); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusNoContent, nil)
}

func configToNERDTO(c *repository.NERConfig) nerConfigDTO {
	dto := nerConfigDTO{
		Enabled:       c.Enabled,
		Model:         c.Model,
		EntityTypes:   c.EntityTypes,
		BatchSize:     c.BatchSize,
		MinConfidence: c.MinConfidence,
		HasAPIKey:     c.APIKeyEncrypted != "",
	}
	if c.APIKeySetAt != nil {
		s := c.APIKeySetAt.UTC().Format("2006-01-02T15:04:05.999Z07:00")
		dto.APIKeySetAt = &s
	}
	return dto
}

func entityToDTO(e *repository.Entity) entityDTO {
	return entityDTO{
		ID:          e.ID.String(),
		EntityType:  e.EntityType,
		EntityValue: e.EntityValue,
		StartOffset: e.StartOffset,
		EndOffset:   e.EndOffset,
		Confidence:  e.Confidence,
		IsPII:       e.IsPII,
		Source:      e.Source,
		DetectedAt:  e.DetectedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00"),
	}
}

func entityCorrectionToDTO(c *repository.EntityCorrection) entityCorrectionDTO {
	return entityCorrectionDTO{
		ID:            c.ID.String(),
		DocumentID:    c.DocumentID.String(),
		VersionID:     c.VersionID.String(),
		OriginalType:  c.OriginalType,
		CorrectedType: c.CorrectedType,
		EntityValue:   c.EntityValue,
		Action:        c.Action,
		Note:          c.Note,
		CorrectedBy:   c.CorrectedBy.String(),
		CreatedAt:     c.CreatedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00"),
	}
}
