// Package handler exposes REST endpoints for notifications.
package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/notification/internal/model"
	"github.com/aieera/sedoc/services/notification/internal/service"
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
	mux.HandleFunc("GET /api/v1/notifications", h.list)
	mux.HandleFunc("PATCH /api/v1/notifications/{id}/read", h.markRead)
	mux.HandleFunc("POST /api/v1/notifications/read-all", h.markAllRead)
	mux.HandleFunc("GET /api/v1/notifications/unread-count", h.unreadCount)
	mux.HandleFunc("GET /api/v1/notifications/preferences", h.getPreferences)
	mux.HandleFunc("PUT /api/v1/notifications/preferences", h.updatePreferences)
	// ADR 0086 — matrix preferences, snooze, DND.
	h.RegisterPrefs(mux)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID and X-User-ID required")
		return
	}
	var readFilter *bool
	if v := r.URL.Query().Get("read"); v != "" {
		b := v == "true"
		readFilter = &b
	}
	limit := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = v
	}
	notifs, err := h.svc.List(r.Context(), tenantID, userID, readFilter, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	if notifs == nil {
		notifs = []*model.Notification{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":       notifs,
		"total_count": len(notifs),
	})
}

func (h *Handler) markRead(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	id := r.PathValue("id")
	if err := h.svc.MarkRead(r.Context(), tenantID, userID, id); err != nil {
		writeError(w, http.StatusInternalServerError, "mark read failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) markAllRead(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	if err := h.svc.MarkAllRead(r.Context(), tenantID, userID); err != nil {
		writeError(w, http.StatusInternalServerError, "mark all read failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) unreadCount(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	count, err := h.svc.UnreadCount(r.Context(), tenantID, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "count failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"count": count})
}

func (h *Handler) getPreferences(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	pref, err := h.svc.GetPreference(r.Context(), tenantID, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "get prefs failed")
		return
	}
	writeJSON(w, http.StatusOK, pref)
}

func (h *Handler) updatePreferences(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	var pref model.UserPreference
	if err := json.NewDecoder(r.Body).Decode(&pref); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	pref.TenantID = tenantID
	pref.UserID = userID
	if err := h.svc.UpdatePreference(r.Context(), &pref); err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	writeJSON(w, http.StatusOK, pref)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// silence unused
var _ = strings.TrimSpace
