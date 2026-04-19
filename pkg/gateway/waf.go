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

// SecurityHeadersMiddleware adds hardened security headers to every response.
func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("X-XSS-Protection", "0") // deprecated but set to 0 per OWASP
		next.ServeHTTP(w, r)
	})
}

// silence unused
var _ = io.Discard
var _ = strings.TrimSpace
