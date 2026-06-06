// Package handler exposes REST endpoints for the audit service.
package handler

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/audit/internal/model"
	"github.com/aieera/sedoc/services/audit/internal/service"
)

// Handler holds HTTP route handlers.
type Handler struct {
	svc *service.Service
	log zerolog.Logger
}

// New constructs a Handler.
func New(svc *service.Service, log zerolog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

// Register mounts routes.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/audit/events", h.listEvents)
	mux.HandleFunc("GET /api/v1/audit/export", h.exportCSV)
	mux.HandleFunc("POST /api/v1/audit/verify-integrity", h.verifyIntegrity)
	mux.HandleFunc("POST /api/v1/audit/data-subject/export", h.dataSubjectExport)
	mux.HandleFunc("POST /api/v1/audit/data-subject/anonymize", h.dataSubjectAnonymize)
	// ADR 0103 — audit-trail visualization aggregation.
	mux.HandleFunc("GET /api/v1/audit/documents/{document_id}/viz", h.documentAuditViz)
}

func (h *Handler) listEvents(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "unauthenticated: no tenant on session")
		return
	}
	f := model.ListFilter{
		TenantID:     tenantID,
		Actor:        r.URL.Query().Get("actor"),
		Action:       r.URL.Query().Get("action"),
		ResourceType: r.URL.Query().Get("resource_type"),
		ResourceID:   r.URL.Query().Get("resource_id"),
		PageToken:    r.URL.Query().Get("page_token"),
	}
	if ps, err := strconv.Atoi(r.URL.Query().Get("page_size")); err == nil {
		f.PageSize = ps
	}
	if v := r.URL.Query().Get("date_from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.DateFrom = &t
		}
	}
	if v := r.URL.Query().Get("date_to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.DateTo = &t
		}
	}
	events, nextToken, err := h.svc.List(r.Context(), f)
	if err != nil {
		h.log.Error().Err(err).Msg("list events")
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events":     events,
		"page_token": nextToken,
	})
}

func (h *Handler) exportCSV(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "unauthenticated: no tenant on session")
		return
	}
	events, _, err := h.svc.List(r.Context(), model.ListFilter{TenantID: tenantID, PageSize: 10000})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "export failed")
		return
	}
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", "attachment; filename=audit_log.csv")
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"id", "timestamp", "actor", "action", "resource_type", "resource_id", "ip_address"})
	for _, e := range events {
		_ = cw.Write([]string{e.ID, e.CreatedAt.Format(time.RFC3339), e.Actor, e.Action, e.ResourceType, e.ResourceID, e.IPAddress})
	}
	cw.Flush()
}

func (h *Handler) verifyIntegrity(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "unauthenticated: no tenant on session")
		return
	}
	result, err := h.svc.VerifyIntegrity(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "verification failed")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type subjectBody struct {
	SubjectID string `json:"subject_id"`
}

func (h *Handler) dataSubjectExport(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	var body subjectBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.SubjectID == "" || tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant_id and subject_id required")
		return
	}
	export, err := h.svc.ExportSubject(r.Context(), tenantID, body.SubjectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "export failed")
		return
	}
	writeJSON(w, http.StatusOK, export)
}

func (h *Handler) dataSubjectAnonymize(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	var body subjectBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.SubjectID == "" || tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant_id and subject_id required")
		return
	}
	count, err := h.svc.AnonymizeSubject(r.Context(), tenantID, body.SubjectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "anonymize failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"anonymized_count": count})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
