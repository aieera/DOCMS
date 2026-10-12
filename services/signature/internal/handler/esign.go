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
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/esign"
	"github.com/aieera/sedoc/services/signature/internal/service"
)

// RegisterESign mounts the connector routes.
func (h *Handler) RegisterESign(mux *http.ServeMux) {
	e := &esignRoutes{svc: h.svc, h: h}
	mux.HandleFunc("POST /api/v1/signatures/esign/send", limitBody(jsonBodyLimit, e.send))
	mux.HandleFunc("GET /api/v1/signatures/esign/connections", e.connections)
	mux.HandleFunc("POST /api/v1/signatures/esign/oauth/start", limitBody(jsonBodyLimit, e.oauthStart))
	mux.HandleFunc("GET /api/v1/signatures/esign/oauth/callback", e.oauthCallback)
	mux.HandleFunc("POST /api/v1/signatures/esign/disconnect", limitBody(jsonBodyLimit, e.disconnect))
	mux.HandleFunc("POST /api/v1/signatures/esign/connections/{provider}/refresh", limitBody(jsonBodyLimit, e.refresh))
	mux.HandleFunc("GET /api/v1/signatures/esign/envelopes", e.envelopes)
	// Per-tenant OAuth client credentials (paste-from-UI flow). Admin
	// fills the modal with Integration Key + Secret Key + environment
	// before triggering OAuth start. Secret never travels back to the
	// browser — GET returns has_secret as a presence flag only.
	mux.HandleFunc("GET /api/v1/signatures/esign/provider-config/{provider}", e.getProviderConfig)
	mux.HandleFunc("PUT /api/v1/signatures/esign/provider-config/{provider}", limitBody(jsonBodyLimit, e.putProviderConfig))
	mux.HandleFunc("DELETE /api/v1/signatures/esign/provider-config/{provider}", e.deleteProviderConfig)
}

// RegisterESignWebhook mounts the vendor webhook. It is registered apart from
// RegisterESign because it is the one e-sign route that is NOT behind session
// authentication: vendors don't carry our session cookie, so the request is
// authenticated by the provider's body HMAC instead. The webhook URL is
// tenant-scoped; the admin pastes the per-tenant URL into the vendor portal
// at integration-setup time.
func (h *Handler) RegisterESignWebhook(mux *http.ServeMux) {
	e := &esignRoutes{svc: h.svc, h: h}
	mux.HandleFunc("POST /api/v1/signatures/esign/webhook/{provider}/{tenant}", limitBody(documentBodyLimit, e.webhook))
}

type esignRoutes struct {
	svc *service.Service
	h   *Handler
}

// ----- Send --------------------------------------------------------

// sendBody is the e-sign send contract (DEC-17 / S-1, S-14). The client
// names the request; the server derives the document bytes from the
// request's own (document_id, version_id) and the recipients from its signer
// rows. Legacy fields (document_bytes_b64, recipients) are ignored if sent.
type sendBody struct {
	RequestID string         `json:"request_id"`
	Provider  esign.Provider `json:"provider,omitempty"`
	// DocumentName is the file name shown by the vendor. Default document.pdf.
	DocumentName string `json:"document_name,omitempty"`
	Subject      string `json:"subject,omitempty"`
	Message      string `json:"message,omitempty"`
	ReturnURL    string `json:"return_url,omitempty"`
	// EmbeddedSignerIDs are signer ids (from the request) that should get an
	// in-app signing URL instead of the vendor's email.
	EmbeddedSignerIDs []string `json:"embedded_signer_ids,omitempty"`
}

// sendResponse is the 201 body of a successful send.
type sendResponse struct {
	RequestID   string            `json:"request_id"`
	EnvelopeID  string            `json:"envelope_id"`
	Status      string            `json:"status"`
	SigningURLs map[string]string `json:"signing_urls,omitempty"`
}

// actorFromRequest returns the authenticated caller.
func actorFromRequest(r *http.Request) service.Actor {
	u, _ := auth.User(r.Context())
	return service.Actor{
		TenantID: auth.TenantIDString(r),
		UserID:   auth.UserIDString(r),
		Role:     u.Role,
		Email:    u.Email,
	}
}

func (e *esignRoutes) send(w http.ResponseWriter, r *http.Request) {
	actor := actorFromRequest(r)
	if actor.TenantID == "" || actor.UserID == "" {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var body sendBody
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.RequestID == "" {
		writeError(w, http.StatusBadRequest, "request_id required")
		return
	}
	resp, err := e.svc.SendViaProvider(r.Context(), service.SendESignInput{
		Actor: actor, RequestID: body.RequestID, Provider: body.Provider,
		DocumentName: body.DocumentName, Subject: body.Subject, Message: body.Message,
		ReturnURL: body.ReturnURL, EmbeddedSignerIDs: body.EmbeddedSignerIDs,
	})
	if err != nil {
		e.h.writeServiceError(w, err, "esign send failed")
		return
	}
	writeJSON(w, http.StatusCreated, sendResponse{
		RequestID: body.RequestID, EnvelopeID: resp.EnvelopeID,
		Status: resp.Status, SigningURLs: resp.SigningURLs,
	})
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
	if !requireTenantAdmin(w, r) {
		return
	}
	var b oauthStartBody
	if !decodeJSON(w, r, &b) {
		return
	}
	url, err := e.svc.StartOAuth(r.Context(), actorFromRequest(r), b.Provider)
	if err != nil {
		if errors.Is(err, service.ErrForbidden) {
			writeError(w, http.StatusForbidden, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"redirect_url": url})
}

// oauthCallback is the vendor's browser redirect back to us. It runs under
// the admin's session (the session cookie is SameSite=Lax, so it survives
// this top-level navigation); the provider and the single-use nonce come
// from `state`, which the service consumes for this session's tenant and
// user before exchanging the code.
func (e *esignRoutes) oauthCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		esignCallbackError(w, r, "code, state required")
		return
	}
	target, err := e.svc.HandleOAuthCallback(r.Context(), actorFromRequest(r), code, state)
	if err != nil {
		// This is a top-level browser navigation (the vendor redirected
		// the browser here), not an XHR — a raw JSON 400 would leave the
		// user staring at a JSON body. Bounce back into the SPA with the
		// reason so they see a real page + a surfaced error, and so the
		// failure is visible in the URL for support.
		esignCallbackError(w, r, err.Error())
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// esignCallbackError redirects a failed OAuth callback back into the SPA
// integrations page with the reason in a query param.
func esignCallbackError(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r,
		"/admin/integrations?tab=esign&esign_error="+url.QueryEscape(msg),
		http.StatusSeeOther)
}

type disconnectBody struct {
	Provider esign.Provider `json:"provider"`
}

func (e *esignRoutes) disconnect(w http.ResponseWriter, r *http.Request) {
	if !requireTenantAdmin(w, r) {
		return
	}
	var b disconnectBody
	if !decodeJSON(w, r, &b) {
		return
	}
	if err := e.svc.Disconnect(r.Context(), actorFromRequest(r), b.Provider); err != nil {
		e.h.writeServiceError(w, err, "disconnect failed")
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
// vendor portal during connect. The provider's body HMAC, checked with the
// tenant's stored secret before anything else, is what authenticates the
// request; the tenant in the URL is only the lookup key.
//
// Every unauthenticated outcome is the same 401 with the same body
// (S-9); errors after verification are logged, never echoed.
func (e *esignRoutes) webhook(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenant")
	provider := esign.Provider(r.PathValue("provider"))
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if isTooLarge(err) {
			writeTooLarge(w)
			return
		}
		writeError(w, http.StatusBadRequest, "read body")
		return
	}
	if err := e.svc.HandleWebhook(r.Context(), tenantID, provider, r.Header, body); err != nil {
		switch {
		case errors.Is(err, service.ErrWebhookUnauthorized):
			writeError(w, http.StatusUnauthorized, "unauthorized")
		case errors.Is(err, service.ErrInvalidRequest):
			writeError(w, http.StatusBadRequest, "invalid webhook payload")
		default:
			e.h.log.Error().Err(err).Str("provider", string(provider)).Msg("esign webhook ingest failed")
			writeError(w, http.StatusInternalServerError, "webhook processing failed")
		}
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

// requireTenantAdmin gates the provider-credential endpoints. Storing/replacing
// a tenant's DocuSign/Adobe OAuth client_id + client_secret (or wiping it) is an
// admin action; without this any authenticated member could overwrite or delete
// the tenant's e-sign integration credentials.
func requireTenantAdmin(w http.ResponseWriter, r *http.Request) bool {
	switch auth.RoleString(r) {
	case "owner", "admin":
		return true
	default:
		writeError(w, http.StatusForbidden, "admin role required")
		return false
	}
}

func (e *esignRoutes) putProviderConfig(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant required")
		return
	}
	if !requireTenantAdmin(w, r) {
		return
	}
	provider := esign.Provider(r.PathValue("provider"))
	if provider != esign.ProviderDocuSign && provider != esign.ProviderAdobeSign {
		writeError(w, http.StatusBadRequest, "provider must be docusign or adobe_sign")
		return
	}
	var b putProviderConfigBody
	if !decodeJSON(w, r, &b) {
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
	if !requireTenantAdmin(w, r) {
		return
	}
	provider := esign.Provider(r.PathValue("provider"))
	if err := e.svc.DeleteProviderConfig(r.Context(), tenantID, provider); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
