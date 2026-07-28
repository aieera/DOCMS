package handler

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/auth/internal/sso"
)

// OIDCHandler serves /login and /callback for the OAuth 2.0 + OIDC flow.
// Piggy-backs on the main Handler for cookie attributes + logging, same
// pattern as SAMLHandler.
type OIDCHandler struct {
	svc  *sso.OIDCService
	main *Handler
	pool *pgxpool.Pool
}

// NewOIDCHandler wires the handler.
func NewOIDCHandler(svc *sso.OIDCService, main *Handler, pool *pgxpool.Pool) *OIDCHandler {
	return &OIDCHandler{svc: svc, main: main, pool: pool}
}

// Login starts the OIDC flow: 302 to the IdP authorize endpoint with PKCE.
func (h *OIDCHandler) Login(w http.ResponseWriter, r *http.Request) {
	slug := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "tenant_slug")))
	tenantID, err := h.resolveTenantID(r, slug)
	if err != nil {
		h.writeError(w, err)
		return
	}
	url, state, err := h.svc.BuildAuthorizeURL(r.Context(), slug, tenantID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	// Bind the flow to this browser: ExchangeCode requires the callback's state
	// param to match this cookie (login-CSRF / fixation defense). SameSite=Lax
	// so it survives the top-level GET redirect back from the IdP.
	http.SetCookie(w, &http.Cookie{
		Name:     "oidc_state",
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.main.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
	})
	http.Redirect(w, r, url, http.StatusFound)
}

// Callback receives the code + state from the IdP. ExchangeCode handles
// PKCE verification, token exchange, ID token validation, and provisioning.
func (h *OIDCHandler) Callback(w http.ResponseWriter, r *http.Request) {
	slug := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "tenant_slug")))
	tenantID, err := h.resolveTenantID(r, slug)
	if err != nil {
		h.writeError(w, err)
		return
	}
	ip, ua := samlClientMeta(r)
	result, err := h.svc.ExchangeCode(r.Context(), r, slug, tenantID, ip, ua)
	if err != nil {
		h.writeError(w, err)
		return
	}
	h.main.issueSessionCookies(w, result.SessionToken, result.ExpiresAt)
	target := result.RedirectTo
	if target == "" {
		target = "/"
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// resolveTenantID mirrors the SAML handler's lookup.
func (h *OIDCHandler) resolveTenantID(r *http.Request, slug string) (uuid.UUID, error) {
	if slug == "" {
		return uuid.Nil, vdmserr.Validation("tenant_slug", "required")
	}
	var id uuid.UUID
	err := h.pool.QueryRow(r.Context(),
		`SELECT id FROM organizations WHERE slug = $1 AND deleted_at IS NULL`,
		slug,
	).Scan(&id)
	if err != nil {
		return uuid.Nil, vdmserr.ErrUnauthorized
	}
	return id, nil
}

func (h *OIDCHandler) writeError(w http.ResponseWriter, err error) {
	body := vdmserr.ToHTTPError(err, "")
	http.Error(w, body.Message, body.Code)
}
