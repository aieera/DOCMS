// Package handler exposes internal REST endpoints for the billing service.
// All endpoints require admin API key auth (X-API-Key header).
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/services/billing/internal/model"
	"github.com/vaultdms/vaultdms/services/billing/internal/service"
	stripehandler "github.com/vaultdms/vaultdms/services/billing/internal/stripe"
)

// Handler holds HTTP route handlers.
type Handler struct {
	svc       *service.Service
	stripeWH  *stripehandler.WebhookHandler
	log       zerolog.Logger
	apiKey    string
}

// New constructs a Handler. apiKey is sourced from cfg.InternalAPIKey by
// the caller so secret plumbing lives entirely in main.go.
func New(svc *service.Service, stripeWH *stripehandler.WebhookHandler, log zerolog.Logger, apiKey string) *Handler {
	return &Handler{
		svc: svc, stripeWH: stripeWH, log: log,
		apiKey: apiKey,
	}
}

// Register mounts routes.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /internal/v1/tenants/provision", h.requireAPIKey(h.provision))
	mux.HandleFunc("POST /internal/v1/stripe/webhook", h.stripeWH.HandleWebhook)
	mux.HandleFunc("GET /internal/v1/tenants/{tenantId}/subscription", h.requireAPIKey(h.getSubscription))
	mux.HandleFunc("GET /internal/v1/tenants/{tenantId}/features", h.requireAPIKey(h.getFeatures))
	mux.HandleFunc("PUT /internal/v1/tenants/{tenantId}/features", h.requireAPIKey(h.updateFeatures))
	mux.HandleFunc("GET /internal/v1/plans", h.requireAPIKey(h.listPlans))
	// Wave 20 — tenant lifecycle.
	mux.HandleFunc("GET /internal/v1/tenants", h.requireAPIKey(h.listTenants))
	mux.HandleFunc("POST /internal/v1/tenants/{tenantId}/deprovision", h.requireAPIKey(h.deprovisionTenant))
	mux.HandleFunc("POST /internal/v1/tenants/{tenantId}/undo-deprovision", h.requireAPIKey(h.undoDeprovision))
}

func (h *Handler) requireAPIKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.apiKey != "" && r.Header.Get("X-API-Key") != h.apiKey {
			writeError(w, http.StatusUnauthorized, "invalid api key")
			return
		}
		next(w, r)
	}
}

func (h *Handler) provision(w http.ResponseWriter, r *http.Request) {
	var req model.ProvisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.OrgName == "" || req.AdminEmail == "" {
		writeError(w, http.StatusBadRequest, "org_name and admin_email required")
		return
	}
	if req.Plan == "" {
		req.Plan = "standard"
	}
	if req.Region == "" {
		req.Region = "us-east-1"
	}
	result, err := h.svc.Provision(r.Context(), req)
	if err != nil {
		h.log.Error().Err(err).Msg("provision failed")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (h *Handler) getSubscription(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenantId")
	sub, err := h.svc.GetSubscription(r.Context(), tenantID)
	if err != nil || sub == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, sub)
}

func (h *Handler) getFeatures(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenantId")
	flags, err := h.svc.GetFeatureFlags(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, flags)
}

func (h *Handler) updateFeatures(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenantId")
	var flags model.FeatureFlags
	if err := json.NewDecoder(r.Body).Decode(&flags); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := h.svc.UpdateFeatureFlags(r.Context(), tenantID, &flags); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, flags)
}

func (h *Handler) listPlans(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, model.DefaultPlans)
}

// ---- Tenant lifecycle (Wave 20) -------------------------------------------

// listTenants returns every organization the admin can see. Thin
// projection — the admin UI only needs enough to render the Tenants
// table; a detail view hits GET /{tenantId}/subscription.
func (h *Handler) listTenants(w http.ResponseWriter, r *http.Request) {
	tenants, err := h.svc.ListTenants(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tenants)
}

// deprovisionTenant soft-deletes a tenant + schedules hard-dispose
// after graceDays (default 30). Idempotent: re-calling on an
// already-scheduled tenant is a no-op.
func (h *Handler) deprovisionTenant(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenantId")
	if err := h.svc.Deprovision(r.Context(), tenantID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "scheduled"})
}

// undoDeprovision reverses a soft-delete iff hard-dispose hasn't
// fired yet. Returns 409 when the grace period elapsed (disposed_at
// is non-NULL) — the crypto-shred is by definition irreversible.
func (h *Handler) undoDeprovision(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenantId")
	if err := h.svc.UndoDeprovision(r.Context(), tenantID); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restored"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
