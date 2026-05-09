// Package handler exposes the auth service's HTTP surface (JSON over
// chi router). A session-validation middleware is also exported for every
// other VaultDMS service to import.
package handler

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/auth"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/auth/internal/service"
)

// Handler carries the service + logger + cookie settings.
type Handler struct {
	svc          *service.Service
	log          zerolog.Logger
	cookieName   string
	cookieSecure bool
}

// Config is the handler's DI shape.
type Config struct {
	Service      *service.Service
	Logger       zerolog.Logger
	CookieName   string // default "dms_session"
	CookieSecure bool   // true in prod; false for local HTTP-only testing
}

// New constructs a Handler.
func New(cfg Config) *Handler {
	name := cfg.CookieName
	if name == "" {
		name = "dms_session"
	}
	return &Handler{
		svc:          cfg.Service,
		log:          cfg.Logger,
		cookieName:   name,
		cookieSecure: cfg.CookieSecure,
	}
}

// ---- HTTP helpers ---------------------------------------------------------

// writeJSON marshals v, sets Content-Type, writes status. Errors on encode
// are logged; we don't try to recover (status already written).
func (h *Handler) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.log.Warn().Err(err).Msg("json encode")
	}
}

// writeError maps domain errors to HTTP. Never includes stack traces in the
// body. Correlation id pulled from the request context where available.
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	corr := auth.GetCorrelationID(r.Context())
	body := vdmserr.ToHTTPError(err, corr)
	if body.Code >= 500 {
		h.log.Error().Str("correlation_id", corr).Err(err).Msg("auth 5xx")
	}

	// Special cases: login/MFA/API-key failures ALWAYS surface the same
	// generic message regardless of the underlying cause.
	if errors.Is(err, service.ErrInvalidCredentials) {
		body.Code = http.StatusUnauthorized
		body.Type = "UNAUTHORIZED"
		body.Message = "invalid credentials"
	} else if errors.Is(err, service.ErrAccountLocked) {
		body.Code = http.StatusTooManyRequests
		body.Type = "RATE_LIMITED"
		body.Message = "account temporarily locked"
	} else if errors.Is(err, service.ErrMethodUnavailable) {
		// ADR 0063 — method picker hit a path the deploy / tenant
		// hasn't enabled, or the user hasn't enrolled. 409 lets the
		// frontend hide the method without re-rendering an error.
		body.Code = http.StatusConflict
		body.Type = "METHOD_UNAVAILABLE"
		body.Message = err.Error()
	}

	h.writeJSON(w, body.Code, body)
}

// readJSON decodes the request body with a byte limit.
func (h *Handler) readJSON(r *http.Request, v any) error {
	if r.ContentLength > 64*1024 {
		return vdmserr.Validation("body", "request too large")
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return vdmserr.Validation("body", "invalid JSON")
	}
	return nil
}

// ---- Token helpers --------------------------------------------------------

// extractBearer returns (token, isAPIKey). Checks Authorization: Bearer then
// the session cookie. API keys are recognized by the "vdms_" prefix.
func (h *Handler) extractBearer(r *http.Request) (string, bool) {
	if v := r.Header.Get("Authorization"); strings.HasPrefix(v, "Bearer ") {
		token := strings.TrimSpace(strings.TrimPrefix(v, "Bearer "))
		return token, strings.HasPrefix(token, "vdms_")
	}
	if c, err := r.Cookie(h.cookieName); err == nil {
		return c.Value, false
	}
	return "", false
}

// setSessionCookie issues the canonical cookie. Secure defaults to the
// handler's configured value (true in prod).
func (h *Handler) setSessionCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     h.cookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
}

// clearSessionCookie issues a Max-Age=0 cookie with matching attributes.
func (h *Handler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     h.cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
}

// CSRFCookieName is the browser-readable token the frontend echoes
// back in the X-CSRF-Token header on mutating requests. Double-submit
// pattern: a cross-origin attacker can't read the cookie (SameSite)
// and can't write the matching header, so forged requests fail.
const CSRFCookieName = "dms_csrf"

// setCSRFCookie mints and issues a fresh double-submit token paired
// with the session. It is NOT httpOnly (the frontend axios
// interceptor must read and echo it) but IS Secure + SameSite=Strict
// so neither XSS-originated JS from another origin nor a cross-site
// POST can forge a valid pair.
func (h *Handler) setCSRFCookie(w http.ResponseWriter, expiresAt time.Time) string {
	// 32 random bytes → hex. Token is opaque to the server; the CSRF
	// middleware only compares string equality with the header.
	raw := make([]byte, 32)
	if _, err := cryptorand.Read(raw); err != nil {
		// Fall back to a time-seeded value rather than a zero cookie.
		// CSRF middleware will still enforce equality; this path is
		// only hit if the OS entropy source is broken.
		raw = []byte(time.Now().UTC().Format(time.RFC3339Nano))
	}
	token := hex.EncodeToString(raw)
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
		HttpOnly: false, // browser-readable on purpose
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
	return token
}

// clearCSRFCookie pairs with clearSessionCookie on logout.
func (h *Handler) clearCSRFCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: false,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
}

// clientMeta returns (ip, user-agent) suitable for audit logging. The IP is
// returned bracket-free (IPv6 host) so it round-trips into Postgres INET
// columns via NULLIF('', '')::inet.
func clientMeta(r *http.Request) (string, string) {
	ip := r.Header.Get("X-Forwarded-For")
	if i := strings.IndexByte(ip, ','); i > 0 {
		ip = strings.TrimSpace(ip[:i])
	}
	if ip == "" {
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			ip = host
		} else {
			ip = r.RemoteAddr
		}
	}
	return ip, r.UserAgent()
}

// requireUser pulls UserID/TenantID from ctx (set by AuthMiddleware).
// Returns ErrUnauthorized if not authenticated.
func requireUser(r *http.Request) (tenantID, userID uuid.UUID, role string, err error) {
	tenantID, err = auth.GetTenantID(r.Context())
	if err != nil {
		return uuid.Nil, uuid.Nil, "", vdmserr.ErrUnauthorized
	}
	userID, err = auth.GetUserID(r.Context())
	if err != nil {
		return uuid.Nil, uuid.Nil, "", vdmserr.ErrUnauthorized
	}
	return tenantID, userID, auth.GetUserRole(r.Context()), nil
}
