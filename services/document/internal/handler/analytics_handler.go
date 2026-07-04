// Analytics REST (ADR 0119) — governed query API + saved reports.
//
//	GET    /api/v1/analytics/datasets            — registry description (builder UI)
//	POST   /api/v1/analytics/query               — run an ad-hoc governed query
//	GET    /api/v1/analytics/reports             — list saved reports
//	POST   /api/v1/analytics/reports             — create
//	GET    /api/v1/analytics/reports/{id}        — fetch
//	PUT    /api/v1/analytics/reports/{id}        — update
//	DELETE /api/v1/analytics/reports/{id}        — delete
//	POST   /api/v1/analytics/reports/{id}/run    — execute the stored query
//
// Everything is admin/owner-gated in the service layer.
package handler

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/analytics"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/service"
)

const analyticsBodyLimit = 64 << 10

type AnalyticsHandler struct {
	svc *service.DocumentService
	log zerolog.Logger
}

func NewAnalyticsHandler(svc *service.DocumentService, log zerolog.Logger) *AnalyticsHandler {
	return &AnalyticsHandler{svc: svc, log: log}
}

func (h *AnalyticsHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/analytics/datasets", h.datasets)
	mux.HandleFunc("POST /api/v1/analytics/query", h.query)
	mux.HandleFunc("GET /api/v1/analytics/reports", h.list)
	mux.HandleFunc("POST /api/v1/analytics/reports", h.create)
	mux.HandleFunc("GET /api/v1/analytics/reports/{id}", h.get)
	mux.HandleFunc("PUT /api/v1/analytics/reports/{id}", h.update)
	mux.HandleFunc("DELETE /api/v1/analytics/reports/{id}", h.delete)
	mux.HandleFunc("POST /api/v1/analytics/reports/{id}/run", h.run)
}

func (h *AnalyticsHandler) datasets(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"datasets": analytics.DatasetsDTO()})
}

func (h *AnalyticsHandler) query(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	var q analytics.Query
	if err := json.NewDecoder(io.LimitReader(r.Body, analyticsBodyLimit)).Decode(&q); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	res, err := h.svc.RunAnalyticsQuery(r.Context(), q)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, res)
}

type reportBody struct {
	Name                    string          `json:"name"`
	Description             string          `json:"description"`
	Query                   json.RawMessage `json:"query"`
	ChartType               string          `json:"chart_type"`
	ScheduleEnabled         bool            `json:"schedule_enabled"`
	ScheduleCron            string          `json:"schedule_cron"`
	ScheduleIntervalMinutes int             `json:"schedule_interval_minutes"`
	Channels                []string        `json:"channels"`
}

func (b reportBody) input() service.SavedReportInput {
	return service.SavedReportInput{
		Name: b.Name, Description: b.Description, Query: b.Query,
		ChartType: b.ChartType, ScheduleEnabled: b.ScheduleEnabled,
		ScheduleCron: b.ScheduleCron, ScheduleIntervalMinutes: b.ScheduleIntervalMinutes,
		Channels: b.Channels,
	}
}

func savedReportToDTO(r *model.SavedReport) map[string]any {
	return map[string]any{
		"id":                        r.ID.String(),
		"name":                      r.Name,
		"description":               r.Description,
		"query":                     r.Query,
		"chart_type":                r.ChartType,
		"schedule_enabled":          r.ScheduleEnabled,
		"schedule_cron":             r.ScheduleCron,
		"schedule_interval_minutes": r.ScheduleIntervalMinutes,
		"channels":                  r.Channels,
		"created_by":                r.CreatedBy.String(),
		"created_at":                r.CreatedAt,
		"updated_at":                r.UpdatedAt,
		"last_run_at":               r.LastRunAt,
	}
}

func (h *AnalyticsHandler) pathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return uuid.Nil, false
	}
	return id, true
}

func (h *AnalyticsHandler) list(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	rs, err := h.svc.ListSavedReports(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(rs))
	for i := range rs {
		out = append(out, savedReportToDTO(&rs[i]))
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"reports": out})
}

func (h *AnalyticsHandler) create(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	var body reportBody
	if err := json.NewDecoder(io.LimitReader(r.Body, analyticsBodyLimit)).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	rep, err := h.svc.CreateSavedReport(r.Context(), body.input())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, savedReportToDTO(rep))
}

func (h *AnalyticsHandler) get(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}
	rep, err := h.svc.GetSavedReport(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, savedReportToDTO(rep))
}

func (h *AnalyticsHandler) update(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}
	var body reportBody
	if err := json.NewDecoder(io.LimitReader(r.Body, analyticsBodyLimit)).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	rep, err := h.svc.UpdateSavedReport(r.Context(), id, body.input())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, savedReportToDTO(rep))
}

func (h *AnalyticsHandler) delete(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}
	if err := h.svc.DeleteSavedReport(r.Context(), id); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"status": "deleted"})
}

func (h *AnalyticsHandler) run(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}
	res, _, err := h.svc.RunSavedReport(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, res)
}
