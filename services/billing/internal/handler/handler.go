// Package handler exposes internal REST endpoints for the billing service.
// All endpoints except the Stripe webhook (Stripe-Signature verified) require
// the internal API key (X-API-Key header); without a configured key they
// answer 503.
package handler

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/middleware"
	"github.com/aieera/sedoc/services/billing/internal/model"
	"github.com/aieera/sedoc/services/billing/internal/service"
	stripehandler "github.com/aieera/sedoc/services/billing/internal/stripe"
)

// Handler holds HTTP route handlers.
type Handler struct {
	svc      *service.Service
	stripeWH *stripehandler.WebhookHandler
	log      zerolog.Logger
	apiKey   string
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
}

// requireAPIKey gates the internal routes on X-API-Key. It fails closed:
// with no InternalAPIKey configured every gated route answers 503 (never
// open), and the key is compared in constant time.
func (h *Handler) requireAPIKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.apiKey == "" {
			writeError(w, http.StatusServiceUnavailable, "internal api key not configured")
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-API-Key")), []byte(h.apiKey)) != 1 {
			writeError(w, http.StatusUnauthorized, "invalid api key")
			return
		}
		next(w, r)
	}
}

func (h *Handler) provision(w http.ResponseWriter, r *http.Request) {
	var req model.ProvisionRequest
	if !decodeBody(w, r, &req) {
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
	if !decodeBody(w, r, &flags) {
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

// decodeBody decodes the JSON body into v: 413 when it exceeds the MaxBody
// cap (DEC-19), 400 when malformed.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		if middleware.BodyTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid json")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
