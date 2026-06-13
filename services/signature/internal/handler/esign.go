// ADR 0071 — handler routes for DocuSign / Adobe Sign connectors.
//
//	POST /api/v1/signatures/esign/send                 send via vendor
//	GET  /api/v1/signatures/esign/connections          list tenant's connections
//	POST /api/v1/signatures/esign/oauth/start          {provider}    → 302 to vendor
//	GET  /api/v1/signatures/esign/oauth/callback       vendor redirects here
//	POST /api/v1/signatures/esign/disconnect           {provider}
//	GET  /api/v1/signatures/esign/envelopes            in-progress envelope status tab
//	POST /api/v1/signatures/esign/webhook/{provider}   vendor → us
package handler

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/esign"
	"github.com/aieera/sedoc/services/signature/internal/service"
)

// RegisterESign mounts the connector routes.
func (h *Handler) RegisterESign(mux *http.ServeMux) {
	e := &esignRoutes{svc: h.svc}
	mux.HandleFunc("POST /api/v1/signatures/esign/send", e.send)
	mux.HandleFunc("GET /api/v1/signatures/esign/connections", e.connections)
	mux.HandleFunc("POST /api/v1/signatures/esign/oauth/start", e.oauthStart)
	mux.HandleFunc("GET /api/v1/signatures/esign/oauth/callback", e.oauthCallback)
	mux.HandleFunc("POST /api/v1/signatures/esign/disconnect", e.disconnect)
	mux.HandleFunc("POST /api/v1/signatures/esign/connections/{provider}/refresh", e.refresh)
	mux.HandleFunc("GET /api/v1/signatures/esign/envelopes", e.envelopes)
	// Per-tenant OAuth client credentials (paste-from-UI flow). Admin
	// fills the modal with Integration Key + Secret Key + environment
	// before triggering OAuth start. Secret never travels back to the
	// browser — GET returns has_secret as a presence flag only.
	mux.HandleFunc("GET /api/v1/signatures/esign/provider-config/{provider}", e.getProviderConfig)
	mux.HandleFunc("PUT /api/v1/signatures/esign/provider-config/{provider}", e.putProviderConfig)
	mux.HandleFunc("DELETE /api/v1/signatures/esign/provider-config/{provider}", e.deleteProviderConfig)
	// Webhook URL is tenant-scoped because vendors don't carry our
	// session cookie. Admin pastes the per-tenant URL into the
	// vendor portal at integration-setup time.
	mux.HandleFunc("POST /api/v1/signatures/esign/webhook/{provider}/{tenant}", e.webhook)
}

type esignRoutes struct {
	svc *service.Service
}

// ----- Send --------------------------------------------------------

type sendBody struct {
	RequestID        string            `json:"request_id"`
	Provider         esign.Provider    `json:"provider"`
	DocumentName     string            `json:"document_name"`
	DocumentBytesB64 string            `json:"document_bytes_b64"`
	Recipients       []esign.Recipient `json:"recipients"`
	Subject          string            `json:"subject"`
	Message          string            `json:"message"`
	ReturnURL        string            `json:"return_url"`
}

func (e *esignRoutes) send(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant required")
		return
	}
	var body sendBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.RequestID == "" || body.Provider == "" || len(body.Recipients) == 0 || body.DocumentBytesB64 == "" {
		writeError(w, http.StatusBadRequest, "request_id + provider + recipients + document_bytes_b64 required")
		return
	}
	docBytes, err := base64.StdEncoding.DecodeString(body.DocumentBytesB64)
	if err != nil || len(docBytes) == 0 {
		writeError(w, http.StatusBadRequest, "document_bytes_b64 must be non-empty base64")
		return
	}
	resp, err := e.svc.SendViaProvider(r.Context(), tenantID, body.RequestID, body.Provider,
		body.DocumentName, docBytes, body.Recipients, body.ReturnURL, body.Subject, body.Message)
	if err != nil {
		writeError(w, http.StatusBadGateway, "esign send: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, resp)
}

// ----- Connections / OAuth ----------------------------------------

type connectionRow struct {
	Provider    string `json:"provider"`
	AccountID   string `json:"account_id,omitempty"`
	BaseURI     string `json:"base_uri,omitempty"`
	Scope       string `json:"scope,omitempty"`
	ConnectedAt string `json:"connected_at"`
	ExpiresAt   string `json:"expires_at"`
	// Status is a derived field the UI uses to pick the connection
	// pill colour. Values: 'healthy' (expires_at > now+expiringWindow),
	// 'expiring_soon' (expires within 7 days), 'expired'
	// (expires_at <= now). Computed at read time — no DB column —
	// so the refresh worker doesn't have to update it separately.
	Status string `json:"status"`
}

// expiringWindow is how close to expiry a token must be before the
// status flips from 'healthy' to 'expiring_soon'. Matches the
// background refresh worker's lookahead so the UI never shows
// 'expiring_soon' for a token the worker hasn't already attempted
// to refresh.
const expiringWindow = 7 * 24 * time.Hour

func deriveTokenStatus(expiresAt time.Time) string {
	now := time.Now()
	if !expiresAt.After(now) {
		return "expired"
	}
	if expiresAt.Sub(now) <= expiringWindow {
		return "expiring_soon"
	}
	return "healthy"
}

func (e *esignRoutes) connections(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	if tenantID == "" {
		// Reload-race protection: the FE's axios interceptor stages
		// requests behind /auth/me hydration, but a stale 500 from
		// before that lands is still better as a clean 401 so the
		// retry semantics work and React Query reports correctly.
		writeError(w, http.StatusUnauthorized, "tenant required")
		return
	}
	rows, err := e.svc.ListConnections(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	out := make([]connectionRow, 0, len(rows))
	for _, t := range rows {
		out = append(out, connectionRow{
			Provider: t.Provider, AccountID: t.AccountID, BaseURI: t.BaseURI,
			Scope:       t.Scope,
			ConnectedAt: t.ConnectedAt.Format(time.RFC3339),
			ExpiresAt:   t.ExpiresAt.Format(time.RFC3339),
			Status:      deriveTokenStatus(t.ExpiresAt),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": out})
}

type oauthStartBody struct {
	Provider esign.Provider `json:"provider"`
}

func (e *esignRoutes) oauthStart(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	var b oauthStartBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	url, err := e.svc.StartOAuth(r.Context(), tenantID, b.Provider)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"redirect_url": url})
}

func (e *esignRoutes) oauthCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	userID := auth.UserIDString(r)
	if code == "" || state == "" {
		writeError(w, http.StatusBadRequest, "code, state required")
		return
	}
	// DocuSign / Adobe Sign return `state` and `code` only; the
	// provider name has to be recovered from inside `state` (we
	// encoded it there in AuthorizeURLBuilder). ?provider= remains
	// supported as a fallback for older saved redirect URIs.
	provider := esign.Provider(r.URL.Query().Get("provider"))
	if provider == "" {
		_, parsedProvider, ok := esign.ParseState(state)
		if !ok || parsedProvider == "" {
			writeError(w, http.StatusBadRequest, "state does not encode a provider — re-initiate connect")
			return
		}
		provider = esign.Provider(parsedProvider)
	}
	target, err := e.svc.HandleOAuthCallback(r.Context(), provider, code, state, userID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

type disconnectBody struct {
	Provider esign.Provider `json:"provider"`
}

func (e *esignRoutes) disconnect(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	var b disconnectBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := e.svc.Disconnect(r.Context(), tenantID, b.Provider); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// refresh exchanges the stored refresh token for a fresh access
// token + (when the vendor rotates it) a new refresh token. The
// admin button in the Connections UI hits this directly; the
// background worker calls the same service method on a 30-min tick.
func (e *esignRoutes) refresh(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusUnauthorized, "tenant required")
		return
	}
	provider := esign.Provider(r.PathValue("provider"))
	if provider == "" {
		writeError(w, http.StatusBadRequest, "provider required")
		return
	}
	tok, err := e.svc.RefreshAccessToken(r.Context(), tenantID, provider)
	if err != nil {
		// Caller gets the underlying reason so the UI can decide
		// whether to surface "reconnect" (refresh token revoked) vs
		// "try again" (transport / 5xx). Service layer wraps the
		// classification.
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, connectionRow{
		Provider:    tok.Provider,
		AccountID:   tok.AccountID,
		BaseURI:     tok.BaseURI,
		Scope:       tok.Scope,
		ConnectedAt: tok.ConnectedAt.Format(time.RFC3339),
		ExpiresAt:   tok.ExpiresAt.Format(time.RFC3339),
		Status:      deriveTokenStatus(tok.ExpiresAt),
	})
}

// ----- Envelope status tab ----------------------------------------

func (e *esignRoutes) envelopes(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusUnauthorized, "tenant required")
		return
	}
	reqs, err := e.svc.ListInProgressESign(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"envelopes": reqs})
}

// ----- Webhook ingest --------------------------------------------

// webhook accepts vendor callbacks. Both DocuSign and Adobe Sign
// hit us without our session cookie, so tenant is encoded in the
// URL path — the admin pastes a per-tenant webhook URL into the
// vendor portal during connect. HMAC verification (provider-
// specific) is what authenticates the body; the tenant in the URL
// is just the lookup key, so a wrong tenant + valid HMAC would
// still fail safely (the envelope-id lookup wouldn't match).
func (e *esignRoutes) webhook(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenant")
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant required in path")
		return
	}
	provider := esign.Provider(r.PathValue("provider"))
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body")
		return
	}
	if err := e.svc.HandleWebhook(r.Context(), tenantID, provider, r.Header, body); err != nil {
		// HMAC mismatch returns Unauthorized; everything else 500.
		if err == esign.ErrSignatureMismatch {
			writeError(w, http.StatusUnauthorized, "signature invalid")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ----- Per-tenant provider credentials -----------------------------

type putProviderConfigBody struct {
	ClientID             string `json:"client_id"`
	ClientSecret         string `json:"client_secret"`
	Environment          string `json:"environment"` // sandbox | production
	Region               string `json:"region,omitempty"`
	AuthorizeURLOverride string `json:"authorize_url_override,omitempty"`
	TokenURLOverride     string `json:"token_url_override,omitempty"`
}

func (e *esignRoutes) putProviderConfig(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant required")
		return
	}
	provider := esign.Provider(r.PathValue("provider"))
	if provider != esign.ProviderDocuSign && provider != esign.ProviderAdobeSign {
		writeError(w, http.StatusBadRequest, "provider must be docusign or adobe_sign")
		return
	}
	var b putProviderConfigBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := e.svc.SaveProviderConfig(r.Context(), tenantID, userID, provider,
		b.ClientID, b.ClientSecret, b.Environment, b.Region,
		b.AuthorizeURLOverride, b.TokenURLOverride); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (e *esignRoutes) getProviderConfig(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant required")
		return
	}
	provider := esign.Provider(r.PathValue("provider"))
	cfg, err := e.svc.GetProviderConfig(r.Context(), tenantID, provider)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if cfg == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (e *esignRoutes) deleteProviderConfig(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant required")
		return
	}
	provider := esign.Provider(r.PathValue("provider"))
	if err := e.svc.DeleteProviderConfig(r.Context(), tenantID, provider); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
