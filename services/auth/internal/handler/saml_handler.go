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

// SAMLHandler serves the SP metadata, SP-initiated login redirect, and ACS
// callback. It reuses the main Handler for cookie attributes + logging.
type SAMLHandler struct {
	svc  *sso.Service
	main *Handler
	pool *pgxpool.Pool
}

// NewSAMLHandler constructs a SAML handler piggy-backing on the main one.
func NewSAMLHandler(svc *sso.Service, main *Handler, pool *pgxpool.Pool) *SAMLHandler {
	return &SAMLHandler{svc: svc, main: main, pool: pool}
}

// ---- /metadata ------------------------------------------------------------

// Metadata serves the SP metadata XML. An admin pastes this into the IdP.
// No authentication required — it's public by design.
func (h *SAMLHandler) Metadata(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "tenant_slug")
	xmlBytes, err := h.svc.Metadata(r.Context(), slug)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/samlmetadata+xml")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(xmlBytes)
}

// ---- /login ---------------------------------------------------------------

// Login starts an SP-initiated SSO flow: 302 to the IdP's SSO URL with
// the AuthnRequest attached.
func (h *SAMLHandler) Login(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "tenant_slug")
	tenantID, err := h.resolveTenantID(r, slug)
	if err != nil {
		h.writeError(w, err)
		return
	}
	redirectURL, err := h.svc.BuildAuthnRequest(r.Context(), slug, tenantID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	http.Redirect(w, r, redirectURL, http.StatusFound)
}

// ---- /acs -----------------------------------------------------------------

// ACS is the Assertion Consumer Service. The IdP POSTs the signed assertion
// here; we validate, provision, issue a session, and redirect into the app.
func (h *SAMLHandler) ACS(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "tenant_slug")
	tenantID, err := h.resolveTenantID(r, slug)
	if err != nil {
		h.writeError(w, err)
		return
	}

	ip, ua := samlClientMeta(r)
	result, err := h.svc.ConsumeAssertion(r.Context(), r, slug, tenantID, ip, ua)
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

// ---- helpers --------------------------------------------------------------

// resolveTenantID looks up organizations.id by slug. Returns ErrUnauthorized
// on unknown slug to avoid leaking which slugs exist to anonymous callers.
func (h *SAMLHandler) resolveTenantID(r *http.Request, slug string) (uuid.UUID, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		return uuid.UUID{}, vdmserr.Validation("tenant_slug", "required")
	}
	var id uuid.UUID
	err := h.pool.QueryRow(r.Context(),
		`SELECT id FROM organizations WHERE slug = $1 AND deleted_at IS NULL`,
		slug,
	).Scan(&id)
	if err != nil {
		return uuid.UUID{}, vdmserr.ErrUnauthorized
	}
	return id, nil
}

// writeError surfaces a safe message to the IdP-initiated browser. Details
// go to the server log; the user sees a redirect or a plain-text "auth
// failed" page depending on context.
func (h *SAMLHandler) writeError(w http.ResponseWriter, err error) {
	body := vdmserr.ToHTTPError(err, "")
	http.Error(w, body.Message, body.Code)
}

// samlClientMeta mirrors clientMeta in handler.go but without importing it
// directly, to keep this file self-contained for a potential future split.
func samlClientMeta(r *http.Request) (string, string) {
	ip := r.Header.Get("X-Forwarded-For")
	if i := strings.IndexByte(ip, ','); i > 0 {
		ip = strings.TrimSpace(ip[:i])
	}
	if ip == "" {
		ip = r.RemoteAddr
		if i := strings.LastIndexByte(ip, ':'); i > 0 {
			ip = ip[:i]
		}
	}
	return ip, r.UserAgent()
}
