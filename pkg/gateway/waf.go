package gateway

import (
	"io"
	"net/http"
	"regexp"
	"strings"
)

// MaxBodySize is the global body limit (10 MB).
const MaxBodySize = 10 * 1024 * 1024

var wafPatterns = []*regexp.Regexp{
	// SQL injection
	regexp.MustCompile(`(?i)(\bunion\b.*\bselect\b|\bselect\b.*\bfrom\b.*\bwhere\b|'\s*or\s+'1'\s*=\s*'1|;\s*drop\s+table|--\s*$)`),
	// XSS
	regexp.MustCompile(`(?i)(<script[\s>]|javascript:|on\w+\s*=\s*["']|<\s*img[^>]+onerror)`),
	// Path traversal
	regexp.MustCompile(`\.\./|\.\.\\|%2e%2e[/\\]`),
	// Null bytes
	regexp.MustCompile(`\x00|%00`),
	// Command injection
	regexp.MustCompile("(?i)(;\\s*(ls|cat|rm|wget|curl|bash|sh|nc)\\s|\\|\\s*(ls|cat|rm)|`[^`]+`)"),
}

// WAFMiddleware blocks requests matching common attack patterns.
func WAFMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check URL path + query string.
		target := r.URL.RequestURI()
		for _, p := range wafPatterns {
			if p.MatchString(target) {
				http.Error(w, `{"error":"request blocked by WAF"}`, http.StatusForbidden)
				return
			}
		}
		// Check common headers.
		for _, h := range []string{"User-Agent", "Referer", "X-Forwarded-For"} {
			val := r.Header.Get(h)
			for _, p := range wafPatterns {
				if p.MatchString(val) {
					http.Error(w, `{"error":"request blocked by WAF"}`, http.StatusForbidden)
					return
				}
			}
		}
		// Body size limit.
		r.Body = http.MaxBytesReader(w, r.Body, MaxBodySize)
		next.ServeHTTP(w, r)
	})
}

// Security headers used to live here as SecurityHeadersMiddleware. It
// was never wired into any service — so no response ever carried a
// security header (BUG-08) — and it was unsafe to wire as written: it
// set Strict-Transport-Security unconditionally, which on a plain-HTTP
// deployment pins every browser to https:// for a host that has no TLS
// listener. Superseded by pkg/middleware.SecurityHeaders, which makes
// HSTS conditional on the request actually being HTTPS, ships the CSP
// report-only until an operator opts in, and is configurable through
// pkg/config. Removed rather than deprecated so it cannot be wired up
// by mistake.

// silence unused
var _ = io.Discard
var _ = strings.TrimSpace
