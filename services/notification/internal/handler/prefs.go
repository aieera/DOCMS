// ADR 0086 — handler routes for matrix preferences, snooze, and DND.
package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/vaultdms/vaultdms/services/notification/internal/model"
)

// RegisterPrefs mounts the new preference surface alongside the
// legacy flat /preferences GET/PUT endpoints in Register.
func (h *Handler) RegisterPrefs(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/notifications/preferences/matrix", h.getMatrix)
	mux.HandleFunc("PUT /api/v1/notifications/preferences/matrix", h.putMatrix)
	mux.HandleFunc("PATCH /api/v1/notifications/preferences/cell", h.patchCell)

	mux.HandleFunc("GET /api/v1/notifications/snoozes", h.listSnoozes)
	mux.HandleFunc("POST /api/v1/notifications/snooze", h.createSnooze)
	mux.HandleFunc("DELETE /api/v1/notifications/snooze/{id}", h.deleteSnooze)

	mux.HandleFunc("GET /api/v1/notifications/dnd", h.getDND)
	mux.HandleFunc("PUT /api/v1/notifications/dnd", h.putDND)
	mux.HandleFunc("DELETE /api/v1/notifications/dnd", h.deleteDND)
}

// ----- Matrix -----------------------------------------------------

func (h *Handler) getMatrix(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	userID := r.Header.Get("X-User-ID")
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "tenant + user required")
		return
	}
	cells, err := h.svc.ListMatrix(r.Context(), tenantID, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "matrix lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cells": cells})
}

type matrixBody struct {
	Cells []model.PrefCell `json:"cells"`
}

func (h *Handler) putMatrix(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	userID := r.Header.Get("X-User-ID")
	var body matrixBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := h.svc.ReplaceMatrix(r.Context(), tenantID, userID, body.Cells); err != nil {
		writeError(w, http.StatusInternalServerError, "replace failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cells": body.Cells})
}

func (h *Handler) patchCell(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	userID := r.Header.Get("X-User-ID")
	var c model.PrefCell
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	c.TenantID, c.UserID = tenantID, userID
	if c.Channel == "" || c.EventType == "" {
		writeError(w, http.StatusBadRequest, "channel + event_type required")
		return
	}
	if err := h.svc.UpsertCell(r.Context(), c); err != nil {
		writeError(w, http.StatusInternalServerError, "upsert failed")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// ----- Snoozes ----------------------------------------------------

func (h *Handler) listSnoozes(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	userID := r.Header.Get("X-User-ID")
	rows, err := h.svc.ListActiveSnoozes(r.Context(), tenantID, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"snoozes": rows})
}

type snoozeBody struct {
	EventType       string `json:"event_type"`
	DurationMinutes int    `json:"duration_minutes"`
	Reason          string `json:"reason,omitempty"`
}

func (h *Handler) createSnooze(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	userID := r.Header.Get("X-User-ID")
	var b snoozeBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if b.EventType == "" || b.DurationMinutes <= 0 {
		writeError(w, http.StatusBadRequest, "event_type + duration_minutes required")
		return
	}
	s := model.Snooze{TenantID: tenantID, UserID: userID, EventType: b.EventType, Reason: b.Reason}
	out, err := h.svc.CreateSnooze(r.Context(), s, time.Duration(b.DurationMinutes)*time.Minute)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create failed")
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) deleteSnooze(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	userID := r.Header.Get("X-User-ID")
	id := r.PathValue("id")
	if err := h.svc.DeleteSnooze(r.Context(), tenantID, userID, id); err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ----- DND --------------------------------------------------------

func (h *Handler) getDND(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	userID := r.Header.Get("X-User-ID")
	d, err := h.svc.GetDND(r.Context(), tenantID, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "get failed")
		return
	}
	if d == nil {
		writeJSON(w, http.StatusOK, map[string]any{"dnd": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dnd": d})
}

type dndBody struct {
	Start    string `json:"start"`
	End      string `json:"end"`
	Timezone string `json:"timezone"`
}

func (h *Handler) putDND(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	userID := r.Header.Get("X-User-ID")
	var b dndBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if b.Timezone == "" {
		b.Timezone = "UTC"
	}
	d := model.DND{TenantID: tenantID, UserID: userID, DNDStart: b.Start, DNDEnd: b.End, Timezone: b.Timezone}
	if err := h.svc.UpsertDND(r.Context(), d); err != nil {
		writeError(w, http.StatusInternalServerError, "upsert failed")
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (h *Handler) deleteDND(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	userID := r.Header.Get("X-User-ID")
	if err := h.svc.DeleteDND(r.Context(), tenantID, userID); err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
