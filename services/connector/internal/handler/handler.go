// Package handler exposes REST endpoints for the connector service.
package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/services/connector/internal/mcp"
	"github.com/vaultdms/vaultdms/services/connector/internal/service"
)

// Handler holds HTTP route handlers.
type Handler struct {
	svc    *service.Service
	mcpSrv *mcp.Server
	log    zerolog.Logger
}

// New constructs a Handler.
func New(svc *service.Service, mcpSrv *mcp.Server, log zerolog.Logger) *Handler {
	return &Handler{svc: svc, mcpSrv: mcpSrv, log: log}
}

// Register mounts all routes.
func (h *Handler) Register(mux *http.ServeMux) {
	// Wave 12.6: DSR subject-erase for connector data.
	mux.HandleFunc("POST /internal/v1/connectors/purge-subject", h.purgeSubject)
	// Webhooks
	mux.HandleFunc("POST /api/v1/webhooks", h.createWebhook)
	mux.HandleFunc("GET /api/v1/webhooks", h.listWebhooks)
	mux.HandleFunc("DELETE /api/v1/webhooks/{id}", h.deleteWebhook)
	mux.HandleFunc("GET /api/v1/webhooks/{id}/deliveries", h.getDeliveryLog)
	mux.HandleFunc("POST /api/v1/webhooks/{id}/rotate-secret", h.rotateSecret)
	mux.HandleFunc("POST /api/v1/webhooks/{id}/deliveries/{deliveryId}/redeliver", h.redeliverDelivery)
	// Connectors
	mux.HandleFunc("GET /api/v1/connectors", h.listConnectors)
	mux.HandleFunc("GET /api/v1/connectors/{provider}", h.getConnector)
	mux.HandleFunc("GET /api/v1/connectors/{provider}/auth-url", h.getAuthURL)
	mux.HandleFunc("POST /api/v1/connectors/{provider}/callback", h.oauthCallback)
	// MCP
	mux.HandleFunc("POST /api/v1/mcp", h.mcpSrv.HandleSSE)
}

// ---- Webhooks -------------------------------------------------------------

type createWebhookBody struct {
	URL    string   `json:"url"`
	Events []string `json:"events"`
}

func (h *Handler) createWebhook(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	userID := r.Header.Get("X-User-ID")
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID and X-User-ID required")
		return
	}
	var body createWebhookBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.URL == "" || len(body.Events) == 0 {
		writeError(w, http.StatusBadRequest, "url and events required")
		return
	}
	wh, err := h.svc.CreateWebhook(r.Context(), tenantID, userID, body.URL, body.Events)
	if err != nil {
		h.log.Error().Err(err).Msg("create webhook")
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, wh)
}

func (h *Handler) listWebhooks(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	list, err := h.svc.ListWebhooks(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handler) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	id := r.PathValue("id")
	if err := h.svc.DeleteWebhook(r.Context(), tenantID, id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getDeliveryLog(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	id := r.PathValue("id")
	log, err := h.svc.GetDeliveryLog(r.Context(), tenantID, id, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	writeJSON(w, http.StatusOK, log)
}

// Wave 12.6: DSR subject erase on connector-side data.
//
// Current schema: connector_configs is keyed by (tenant_id,
// provider) — OAuth tokens are stored tenant-scoped in the
// encrypted `config` JSONB. There is no per-user grant tracking
// (no `granted_by` column), so "purge this subject's connector
// data" has nothing to target. We log the call for audit and
// return `purged: 0` with an explanation so the workflow's
// cross-service summary reflects reality.
//
// A future schema change (add `granted_by UUID` + per-user
// revocation path) would make this endpoint non-vacuous. Logged
// as Wave 12.6b.
type purgeConnectorBody struct {
	TenantID  string `json:"tenant_id"`
	SubjectID string `json:"subject_id"`
}

func (h *Handler) purgeSubject(w http.ResponseWriter, r *http.Request) {
	var body purgeConnectorBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.TenantID == "" || body.SubjectID == "" {
		writeError(w, http.StatusBadRequest, "tenant_id and subject_id required")
		return
	}
	h.log.Info().
		Str("tenant_id", body.TenantID).
		Str("subject_id", body.SubjectID).
		Msg("dsr connector purge: no per-user grant tracking in current schema; nothing to scrub")
	writeJSON(w, http.StatusOK, map[string]any{
		"purged": 0,
		"note":   "connector_configs is tenant-scoped; per-user OAuth grants not yet modeled (Wave 12.6b)",
	})
}

func (h *Handler) rotateSecret(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	id := r.PathValue("id")
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID required")
		return
	}
	wh, err := h.svc.RotateSecret(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if wh == nil {
		writeError(w, http.StatusNotFound, "webhook not found")
		return
	}
	writeJSON(w, http.StatusOK, wh)
}

func (h *Handler) redeliverDelivery(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	deliveryID := r.PathValue("deliveryId")
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID required")
		return
	}
	d, err := h.svc.RedeliverDelivery(r.Context(), tenantID, deliveryID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if d == nil {
		writeError(w, http.StatusNotFound, "delivery not found")
		return
	}
	writeJSON(w, http.StatusAccepted, d)
}

// ---- Connectors -----------------------------------------------------------

func (h *Handler) listConnectors(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	list, err := h.svc.ListConnectors(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handler) getConnector(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	provider := r.PathValue("provider")
	cc, err := h.svc.GetConnector(r.Context(), tenantID, provider)
	if err != nil || cc == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, cc)
}

func (h *Handler) getAuthURL(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "stub", "provider": r.PathValue("provider")})
}

func (h *Handler) oauthCallback(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "stub", "provider": r.PathValue("provider")})
}

// ---- helpers --------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

var _ = strings.TrimSpace
