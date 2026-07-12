// Workspace templates REST (ADR 0118). Hand-written mux (same idiom as
// tasks/annotations/sync — this service's non-grpc-gateway surface):
//
//	GET    /api/v1/templates                  — gallery list
//	POST   /api/v1/templates                  — create (admin/owner)
//	GET    /api/v1/templates/{id}             — fetch one
//	PUT    /api/v1/templates/{id}             — update (admin/owner)
//	DELETE /api/v1/templates/{id}             — delete (admin/owner)
//	GET    /api/v1/templates/{id}/variables   — variables the provision dialog must prompt for
//	POST   /api/v1/templates/{id}/provision   — scaffold into a workspace
package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/service"
)

type TemplatesHandler struct {
	svc *service.DocumentService
	log zerolog.Logger
}

func NewTemplatesHandler(svc *service.DocumentService, log zerolog.Logger) *TemplatesHandler {
	return &TemplatesHandler{svc: svc, log: log}
}

func (h *TemplatesHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/templates", h.list)
	mux.HandleFunc("POST /api/v1/templates", h.create)
	mux.HandleFunc("GET /api/v1/templates/{id}", h.get)
	mux.HandleFunc("PUT /api/v1/templates/{id}", h.update)
	mux.HandleFunc("DELETE /api/v1/templates/{id}", h.delete)
	mux.HandleFunc("GET /api/v1/templates/{id}/variables", h.variables)
	mux.HandleFunc("POST /api/v1/templates/{id}/provision", h.provision)
}

type templateBody struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Definition  json.RawMessage `json:"definition"`
}

func templateToDTO(t *model.WorkspaceTemplate) map[string]any {
	return map[string]any{
		"id":          t.ID.String(),
		"name":        t.Name,
		"description": t.Description,
		"definition":  t.Definition,
		"created_by":  t.CreatedBy.String(),
		"created_at":  t.CreatedAt,
		"updated_at":  t.UpdatedAt,
	}
}

func (h *TemplatesHandler) pathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return uuid.Nil, false
	}
	return id, true
}

func (h *TemplatesHandler) list(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	ts, err := h.svc.ListTemplates(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(ts))
	for i := range ts {
		out = append(out, templateToDTO(&ts[i]))
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"templates": out})
}

func (h *TemplatesHandler) create(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	var body templateBody
	// Definition size is capped at the model layer too; the reader
	// limit stops an oversized upload before it's buffered.
	if err := json.NewDecoder(io.LimitReader(r.Body, model.TemplateMaxBytes+4096)).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json or too large"))
		return
	}
	t, err := h.svc.CreateTemplate(r.Context(), service.TemplateInput{
		Name: body.Name, Description: body.Description, Definition: body.Definition,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, templateToDTO(t))
}

func (h *TemplatesHandler) get(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}
	t, err := h.svc.GetTemplate(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, templateToDTO(t))
}

func (h *TemplatesHandler) update(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}
	var body templateBody
	if err := json.NewDecoder(io.LimitReader(r.Body, model.TemplateMaxBytes+4096)).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json or too large"))
		return
	}
	t, err := h.svc.UpdateTemplate(r.Context(), id, service.TemplateInput{
		Name: body.Name, Description: body.Description, Definition: body.Definition,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, templateToDTO(t))
}

func (h *TemplatesHandler) delete(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}
	if err := h.svc.DeleteTemplate(r.Context(), id); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"status": "deleted"})
}

func (h *TemplatesHandler) variables(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}
	t, err := h.svc.GetTemplate(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	def, err := model.ParseTemplateDefinition(t.Definition)
	if err != nil {
		writeErr(w, r, vdmserr.Validation("definition", err.Error()))
		return
	}
	vars := def.Variables()
	if vars == nil {
		vars = []string{}
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"variables": vars})
}

type provisionBody struct {
	WorkspaceID    string            `json:"workspace_id"`
	ParentFolderID string            `json:"parent_folder_id,omitempty"`
	Variables      map[string]string `json:"variables,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
}

func (h *TemplatesHandler) provision(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}
	var body provisionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	wsID, err := uuid.Parse(body.WorkspaceID)
	if err != nil {
		writeErr(w, r, vdmserr.Validation("workspace_id", "invalid uuid"))
		return
	}
	in := service.ProvisionInput{
		TemplateID:  id,
		WorkspaceID: wsID,
		Variables:   body.Variables,
	}
	// Idempotency key: standard header wins, body field is the fallback for
	// clients that can't set headers. Empty = provision every time.
	if key := strings.TrimSpace(r.Header.Get("Idempotency-Key")); key != "" {
		in.IdempotencyKey = key
	} else {
		in.IdempotencyKey = strings.TrimSpace(body.IdempotencyKey)
	}
	if body.ParentFolderID != "" {
		pid, err := uuid.Parse(body.ParentFolderID)
		if err != nil {
			writeErr(w, r, vdmserr.Validation("parent_folder_id", "invalid uuid"))
			return
		}
		in.ParentFolderID = &pid
	}
	if in.Variables == nil {
		in.Variables = map[string]string{}
	}
	res, err := h.svc.ProvisionFromTemplate(r.Context(), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, res)
}
