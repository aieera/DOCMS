// Package middleware: CSRF double-submit validator (Wave 6 Prompt 6.2).
//
// Pattern: the server issues two cookies at login — the httpOnly
// session cookie and a JS-readable `dms_csrf` cookie. On every
// mutating request (POST / PUT / PATCH / DELETE), the frontend reads
// the CSRF cookie and echoes it back in the `X-CSRF-Token` header.
// The middleware rejects the request unless:
//
//   * the CSRF cookie is present AND non-empty, AND
//   * the header is present AND non-empty, AND
//   * they compare equal (constant-time).
//
// Safe methods (GET / HEAD / OPTIONS) are exempt.
//
// API-key callers carrying `Authorization: Bearer vdms_*` are ALSO
// exempt — they never acquired a session cookie, and forging their
// request requires the bearer secret already. The middleware detects
// this by looking at the Authorization header before the cookie
// check.
package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

const (
	// CSRFCookieName is the JS-readable sibling of the httpOnly
	// session cookie. Name pinned; the auth service writes it and
	// every service that enforces CSRF reads this same name.
	CSRFCookieName = "dms_csrf"

	// CSRFHeaderName is the echo header. Axios client interceptor
	// (web/src/api/client.ts) copies the cookie value in.
	CSRFHeaderName = "X-CSRF-Token"
)

// CSRFDoubleSubmit returns middleware that rejects mutating requests
// without a matching CSRF cookie + header pair.
//
// Wrap after SessionAuth: CSRF is meaningless without a session
// identity, and API-key callers bypass SessionAuth entirely so they
// never reach this layer anyway.
func CSRFDoubleSubmit() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isSafeMethod(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			// API-key callers are out of scope for CSRF — they cannot
			// be tricked into sending their bearer via a cross-site
			// form post (no implicit credential inclusion).
			if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer vdms_") {
				next.ServeHTTP(w, r)
				return
			}
			c, err := r.Cookie(CSRFCookieName)
			if err != nil || c == nil || c.Value == "" {
				csrfRejectionsTotal.WithLabelValues("missing_cookie").Inc()
				writeForbidden(w, r, "csrf: missing cookie")
				return
			}
			header := r.Header.Get(CSRFHeaderName)
			if header == "" {
				csrfRejectionsTotal.WithLabelValues("missing_header").Inc()
				writeForbidden(w, r, "csrf: missing header")
				return
			}
			if subtle.ConstantTimeCompare([]byte(header), []byte(c.Value)) != 1 {
				csrfRejectionsTotal.WithLabelValues("token_mismatch").Inc()
				writeForbidden(w, r, "csrf: token mismatch")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isSafeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}
