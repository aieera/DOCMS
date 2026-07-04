// Watermark admin API (§5).
//
//	GET    /api/v1/admin/watermark/config
//	PUT    /api/v1/admin/watermark/config
//	GET    /api/v1/admin/watermark/overrides
//	POST   /api/v1/admin/watermark/overrides
//	DELETE /api/v1/admin/watermark/overrides/{id}
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

type WatermarkAdminHandler struct {
	svc *service.DocumentService
	log zerolog.Logger
}

func NewWatermarkAdminHandler(svc *service.DocumentService, log zerolog.Logger) *WatermarkAdminHandler {
	return &WatermarkAdminHandler{svc: svc, log: log}
}

func (h *WatermarkAdminHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/watermark/config", h.getConfig)
	mux.HandleFunc("PUT /api/v1/admin/watermark/config", h.putConfig)
	mux.HandleFunc("GET /api/v1/admin/watermark/overrides", h.listOverrides)
	mux.HandleFunc("POST /api/v1/admin/watermark/overrides", h.createOverride)
	mux.HandleFunc("DELETE /api/v1/admin/watermark/overrides/{id}", h.deleteOverride)
}

type wmConfigDTO struct {
	Enabled     bool   `json:"enabled"`
	Template    string `json:"template"`
	Opacity     int    `json:"opacity"`
	RotationDeg int    `json:"rotation_deg"`
	Tile        bool   `json:"tile"`
	FontSize    int    `json:"font_size"`
	Color       string `json:"color"`
}

func wmConfigToDTO(c model.WatermarkConfig) wmConfigDTO {
	return wmConfigDTO{
		Enabled: c.Enabled, Template: c.Template, Opacity: c.Opacity,
		RotationDeg: c.RotationDeg, Tile: c.Tile, FontSize: c.FontSize, Color: c.Color,
	}
}

type wmOverrideDTO struct {
	ID             string `json:"id"`
	Classification string `json:"classification"`
	Enabled        bool   `json:"enabled"`
	Opacity        *int   `json:"opacity"`
	Tile           *bool  `json:"tile"`
	Force          bool   `json:"force"`
	Description    string `json:"description"`
	CreatedAt      string `json:"created_at"`
}

func wmOverrideToDTO(o model.WatermarkOverride) wmOverrideDTO {
	return wmOverrideDTO{
		ID: o.ID.String(), Classification: o.Classification, Enabled: o.Enabled,
		Opacity: o.Opacity, Tile: o.Tile, Force: o.Force, Description: o.Description,
		CreatedAt: o.CreatedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00"),
	}
}

func (h *WatermarkAdminHandler) getConfig(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	cfg, err := h.svc.GetWatermarkConfig(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, wmConfigToDTO(cfg))
}

func (h *WatermarkAdminHandler) putConfig(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	var body wmConfigDTO
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	cfg, err := h.svc.UpsertWatermarkConfig(r.Context(), model.WatermarkConfig{
		Enabled: body.Enabled, Template: body.Template, Opacity: body.Opacity,
		RotationDeg: body.RotationDeg, Tile: body.Tile, FontSize: body.FontSize, Color: body.Color,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, wmConfigToDTO(cfg))
}

func (h *WatermarkAdminHandler) listOverrides(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	ovs, err := h.svc.ListWatermarkOverrides(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := make([]wmOverrideDTO, 0, len(ovs))
	for _, o := range ovs {
		out = append(out, wmOverrideToDTO(o))
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"overrides": out})
}

type wmOverrideBody struct {
	Classification string `json:"classification"`
	Enabled        bool   `json:"enabled"`
	Opacity        *int   `json:"opacity"`
	Tile           *bool  `json:"tile"`
	Force          bool   `json:"force"`
	Description    string `json:"description"`
}

func (h *WatermarkAdminHandler) createOverride(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	var body wmOverrideBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	created, err := h.svc.UpsertWatermarkOverride(r.Context(), model.WatermarkOverride{
		Classification: body.Classification, Enabled: body.Enabled, Opacity: body.Opacity,
		Tile: body.Tile, Force: body.Force, Description: body.Description,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, wmOverrideToDTO(created))
}

func (h *WatermarkAdminHandler) deleteOverride(w http.ResponseWriter, r *http.Request) {
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
	if err := h.svc.DeleteWatermarkOverride(r.Context(), id); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"status": "deleted"})
}
