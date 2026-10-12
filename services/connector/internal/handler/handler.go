// Package handler exposes REST endpoints for the connector service.
package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/connector/internal/service"
)

// maxConnectorJSONBody bounds a connector control/config JSON request body.
// These carry small structured payloads (webhook config, provider creds,
// folder-intake config, token labels) — never file bytes (the capture bundle
// path has its own 128 MiB limit) — so 1 MiB is generous while stopping an
// unbounded-body memory-exhaustion DoS.
const maxConnectorJSONBody = 1 << 20

// limitedBody wraps the request body in an io.LimitReader so a JSON decode
// can't be made to read an unbounded amount into memory.
func limitedBody(r *http.Request) io.Reader { return io.LimitReader(r.Body, maxConnectorJSONBody) }

// Handler holds HTTP route handlers.
type Handler struct {
	svc *service.Service
	log zerolog.Logger
}

// New constructs a Handler. MCP server was extracted to its own
// service (ADR 0091); the constructor no longer takes mcpSrv.
func New(svc *service.Service, log zerolog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

// Register mounts all routes. webhookAuth optionally wraps the webhook
// management routes with a dual-auth middleware (SessionOrAPIKey with the
// webhooks:manage scope) so machine callers — e.g. an ERP's boot-time
// subscription bootstrap — can manage subscriptions with a Bearer vdms_
// key, while the admin UI keeps using its session cookie. Nil leaves the
// routes on the mux's global auth (session only).
func (h *Handler) Register(mux *http.ServeMux, webhookAuth func(http.Handler) http.Handler) {
	// Wave 12.6: DSR subject-erase for connector data.
	mux.HandleFunc("POST /internal/v1/connectors/purge-subject", h.purgeSubject)
	// Webhooks
	wh := func(pattern string, fn http.HandlerFunc) {
		fn = webhookManagerOnly(fn)
		if webhookAuth != nil {
			mux.Handle(pattern, webhookAuth(fn))
			return
		}
		mux.HandleFunc(pattern, fn)
	}
	wh("POST /api/v1/webhooks", h.createWebhook)
	wh("GET /api/v1/webhooks", h.listWebhooks)
	wh("DELETE /api/v1/webhooks/{id}", h.deleteWebhook)
	wh("GET /api/v1/webhooks/{id}/deliveries", h.getDeliveryLog)
	wh("POST /api/v1/webhooks/{id}/rotate-secret", h.rotateSecret)
	wh("POST /api/v1/webhooks/{id}/test", h.testWebhook)
	wh("POST /api/v1/webhooks/{id}/deliveries/{deliveryId}/redeliver", h.redeliverDelivery)
	// Connectors
	mux.HandleFunc("GET /api/v1/connectors", h.listConnectors)
	mux.HandleFunc("GET /api/v1/connectors/{provider}", h.getConnector)
	mux.HandleFunc("GET /api/v1/connectors/{provider}/auth-url", h.getAuthURL)
	// Vendors redirect to /oauth/callback (no path param) with state +
	// code; the provider is recovered from the HMAC-signed state.
	mux.HandleFunc("GET /api/v1/connectors/oauth/callback", h.connectorOAuthCallback)
	// Per-tenant credentials for native connectors (ADR 0089).
	// Google-specific for now; SaveGoogleConfig validates the body.
	mux.HandleFunc("PUT /api/v1/connectors/google/config", h.putGoogleConfig)
	mux.HandleFunc("POST /api/v1/connectors/google/disconnect", h.disconnectGoogle)
	mux.HandleFunc("POST /api/v1/connectors/google/drive/import", h.importGoogleDrive)
	// Microsoft 365 (ADR 0111). The unified /oauth/callback above
	// dispatches by provider name; no separate callback here.
	mux.HandleFunc("PUT /api/v1/connectors/m365/config", h.putM365Config)
	mux.HandleFunc("GET /api/v1/connectors/m365/auth-url", h.m365AuthURL)
	mux.HandleFunc("POST /api/v1/connectors/m365/disconnect", h.disconnectM365)
	mux.HandleFunc("GET /api/v1/connectors/m365/sites", h.listM365Sites)
	mux.HandleFunc("GET /api/v1/connectors/m365/drives/{drive_id}/items", h.listM365DriveItems)
	// MCP
	// MCP routes moved to services/mcp-server (ADR 0091).
}

// ---- Webhooks -------------------------------------------------------------

type createWebhookBody struct {
	URL    string   `json:"url"`
	Events []string `json:"events"`
}

// requireConnectorAdmin gates connector management — credential writes, OAuth
// connect/disconnect, provider enumeration, watched-folder intake, email
// ingestion, event-stream tokens and scan capture — to admin/owner (H-4,
// M-16). The upstream auth middleware only proves membership. Returns false
// and writes 401 when there is no identity, 403 when the caller lacks the
// role.
func requireConnectorAdmin(w http.ResponseWriter, r *http.Request) bool {
	if _, err := auth.User(r.Context()); err != nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return false
	}
	switch auth.RoleString(r) {
	case "admin", "owner":
		return true
	default:
		writeError(w, http.StatusForbidden, "admin or owner role required")
		return false
	}
}

// webhookManageScope is the API-key scope SessionOrAPIKey enforces on the
// webhook routes.
const webhookManageScope = "webhooks:manage"

// requireWebhookManager gates webhook subscription management. An API-key
// principal (role "api_key") is authorised by its webhooks:manage scope —
// re-checked here so the route stays closed if it is ever mounted without
// SessionOrAPIKey; every other principal (session) must be admin/owner.
func requireWebhookManager(w http.ResponseWriter, r *http.Request) bool {
	if auth.RoleString(r) == "api_key" {
		for _, sc := range auth.GetScopes(r.Context()) {
			if sc == webhookManageScope {
				return true
			}
		}
		writeError(w, http.StatusForbidden, "missing scope: "+webhookManageScope)
		return false
	}
	return requireConnectorAdmin(w, r)
}

// adminOnly wraps a handler with requireConnectorAdmin.
func adminOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireConnectorAdmin(w, r) {
			return
		}
		next(w, r)
	}
}

// webhookManagerOnly wraps a handler with requireWebhookManager.
func webhookManagerOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireWebhookManager(w, r) {
			return
		}
		next(w, r)
	}
}

func (h *Handler) createWebhook(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "unauthenticated: no tenant/user on session")
		return
	}
	var body createWebhookBody
	if err := json.NewDecoder(limitedBody(r)).Decode(&body); err != nil || body.URL == "" || len(body.Events) == 0 {
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
	tenantID := auth.TenantIDString(r)
	list, err := h.svc.ListWebhooks(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handler) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	id := r.PathValue("id")
	if err := h.svc.DeleteWebhook(r.Context(), tenantID, id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getDeliveryLog(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
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
	if err := json.NewDecoder(limitedBody(r)).Decode(&body); err != nil {
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
	tenantID := auth.TenantIDString(r)
	id := r.PathValue("id")
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "unauthenticated: no tenant on session")
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

func (h *Handler) testWebhook(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	id := r.PathValue("id")
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "unauthenticated: no tenant on session")
		return
	}
	d, err := h.svc.SendTestEvent(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if d == nil {
		writeError(w, http.StatusNotFound, "webhook not found")
		return
	}
	writeJSON(w, http.StatusAccepted, d)
}

func (h *Handler) redeliverDelivery(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	deliveryID := r.PathValue("deliveryId")
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "unauthenticated: no tenant on session")
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
	tenantID := auth.TenantIDString(r)
	list, err := h.svc.ListConnectors(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handler) getConnector(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusUnauthorized, "tenant required")
		return
	}
	provider := r.PathValue("provider")
	cc, err := h.svc.GetConnector(r.Context(), tenantID, provider)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if cc == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, cc)
}

// getAuthURL returns the vendor consent URL the browser should be
// redirected to. Tenant must have saved client credentials first.
func (h *Handler) getAuthURL(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant required")
		return
	}
	// Starting a connect binds the tenant's connector account (M-16).
	if !requireConnectorAdmin(w, r) {
		return
	}
	provider := r.PathValue("provider")
	var (
		url string
		err error
	)
	switch provider {
	case "google":
		url, err = h.svc.StartGoogleOAuth(r.Context(), tenantID, auth.UserIDString(r))
	default:
		writeError(w, http.StatusBadRequest, "provider not yet supported: "+provider)
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"auth_url": url})
}

// oauthCallback is the legacy per-provider callback shape preserved
// for compatibility. New connectors should use the unified
// /api/v1/connectors/oauth/callback endpoint above which derives the
// provider from the HMAC-signed state.
func (h *Handler) oauthCallback(w http.ResponseWriter, r *http.Request) {
	h.connectorOAuthCallback(w, r)
}

// ---- helpers --------------------------------------------------------------

// longRunningWrite is the write deadline for synchronous long operations
// (Drive import, scan capture) that may exceed the server WriteTimeout.
const longRunningWrite = 10 * time.Minute

// extendWriteDeadline moves this response's write deadline to now+d — the
// per-route exemption from the server-wide WriteTimeout (DEC-19).
func extendWriteDeadline(w http.ResponseWriter, d time.Duration) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(d))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

var _ = strings.TrimSpace
