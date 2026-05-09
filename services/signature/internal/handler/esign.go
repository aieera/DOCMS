// ADR 0071 — handler routes for DocuSign / Adobe Sign connectors.
//
//   POST /api/v1/signatures/esign/send                 send via vendor
//   GET  /api/v1/signatures/esign/connections          list tenant's connections
//   POST /api/v1/signatures/esign/oauth/start          {provider}    → 302 to vendor
//   GET  /api/v1/signatures/esign/oauth/callback       vendor redirects here
//   POST /api/v1/signatures/esign/disconnect           {provider}
//   GET  /api/v1/signatures/esign/envelopes            in-progress envelope status tab
//   POST /api/v1/signatures/esign/webhook/{provider}   vendor → us
package handler

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/vaultdms/vaultdms/pkg/esign"
	"github.com/vaultdms/vaultdms/services/signature/internal/service"
)

// RegisterESign mounts the connector routes.
func (h *Handler) RegisterESign(mux *http.ServeMux) {
	e := &esignRoutes{svc: h.svc}
	mux.HandleFunc("POST /api/v1/signatures/esign/send", e.send)
	mux.HandleFunc("GET /api/v1/signatures/esign/connections", e.connections)
	mux.HandleFunc("POST /api/v1/signatures/esign/oauth/start", e.oauthStart)
	mux.HandleFunc("GET /api/v1/signatures/esign/oauth/callback", e.oauthCallback)
	mux.HandleFunc("POST /api/v1/signatures/esign/disconnect", e.disconnect)
	mux.HandleFunc("GET /api/v1/signatures/esign/envelopes", e.envelopes)
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
	RequestID  string             `json:"request_id"`
	Provider   esign.Provider     `json:"provider"`
	DocumentName string           `json:"document_name"`
	DocumentBytesB64 string       `json:"document_bytes_b64"`
	Recipients []esign.Recipient  `json:"recipients"`
	Subject    string             `json:"subject"`
	Message    string             `json:"message"`
	ReturnURL  string             `json:"return_url"`
}

func (e *esignRoutes) send(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
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
}

func (e *esignRoutes) connections(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	rows, err := e.svc.ListConnections(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	out := make([]connectionRow, 0, len(rows))
	for _, t := range rows {
		out = append(out, connectionRow{
			Provider: t.Provider, AccountID: t.AccountID, BaseURI: t.BaseURI,
			Scope: t.Scope,
			ConnectedAt: t.ConnectedAt.Format(time.RFC3339),
			ExpiresAt:   t.ExpiresAt.Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": out})
}

type oauthStartBody struct {
	Provider esign.Provider `json:"provider"`
}

func (e *esignRoutes) oauthStart(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
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
	provider := esign.Provider(r.URL.Query().Get("provider"))
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	userID := r.Header.Get("X-User-ID")
	if provider == "" || code == "" || state == "" {
		writeError(w, http.StatusBadRequest, "provider, code, state required")
		return
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
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
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

// ----- Envelope status tab ----------------------------------------

func (e *esignRoutes) envelopes(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
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
