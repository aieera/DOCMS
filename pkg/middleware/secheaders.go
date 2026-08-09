// Package middleware: browser response-hardening headers (BUG-08).
//
// Every HTTP response this platform returns to a browser — the JSON
// API behind the gateway, the few endpoints that stream document
// bytes inline, and any error page produced by net/http itself —
// went out with no security headers at all. This middleware fixes
// that in one place; wrap it OUTERMOST on each service's root
// handler so it also covers responses written by other middleware
// (401s from SessionAuth, 403s from RequireGatewaySignature, 429s
// from the rate limiter) that never reach a route handler.
//
// What each header does and why it is set the way it is:
//
//   - X-Content-Type-Options: nosniff
//     Stops content-type sniffing. Without it a stored file whose
//     declared type is text/plain but whose bytes look like HTML can
//     be executed as HTML in our origin. Always on, no downside.
//
//   - X-Frame-Options: DENY  (+ CSP frame-ancestors 'none')
//     Clickjacking defence. X-Frame-Options is the legacy header that
//     old browsers still honour; frame-ancestors is the modern
//     equivalent and is the one that counts in current browsers. Both
//     are emitted because they cover different browser generations.
//     NOTE: frame-ancestors is IGNORED inside a Report-Only policy, so
//     when the full CSP ships report-only (the default, see below) an
//     additional *enforcing* CSP carrying only frame-ancestors is sent
//     alongside it. Clickjacking protection is therefore real from day
//     one even while the rest of the policy is still in observation
//     mode.
//     Endpoints that are deliberately framed same-origin (the IRM
//     protected-content stream, which the protected-viewer route loads
//     in an <iframe>) override these two headers on their own
//     response — a handler's Header().Set runs after this middleware
//     and wins.
//
//   - Referrer-Policy: no-referrer
//     Chosen over strict-origin-when-cross-origin deliberately. This
//     is a document management system: request paths carry workspace
//     and document UUIDs, and stored documents contain links users
//     click. strict-origin-when-cross-origin would still leak the
//     origin to third parties, and leaks the FULL path on same-origin
//     navigations that end up in logs. no-referrer leaks nothing and
//     the app has no feature that depends on a Referer header (CSRF is
//     enforced with the double-submit cookie in csrf.go, not Referer).
//     Configurable for deployments that need referrer-based analytics.
//
//   - Permissions-Policy: camera=(), microphone=(), geolocation=()
//     Denies the three high-risk device APIs the app never uses, for
//     this document and everything it embeds. Deliberately does NOT
//     mention publickey-credentials-get / -create: those default to
//     'self', and naming them with an empty allowlist would disable
//     passkeys.
//
//   - Strict-Transport-Security
//     ONLY emitted when the request actually arrived over HTTPS. Sent
//     on a plain-HTTP deployment it would pin the browser to https://
//     for a host that has no TLS listener and lock users out for
//     max-age seconds — with includeSubDomains, every sibling host
//     too. Because TLS terminates at the reverse proxy (Caddy / Kong /
//     ingress-nginx) and services see plain HTTP, r.TLS is nil in
//     practice and the decision falls to X-Forwarded-Proto, which the
//     proxy sets itself. Set TrustForwardedProto=false for a service
//     exposed directly with its own TLS listener.
//     `preload` is off by default: submitting a domain to the browser
//     preload list is close to irreversible.
//
//   - Content-Security-Policy
//     Ships REPORT-ONLY by default (CSPEnforce=false). A CSP that is
//     one directive short of correct breaks the whole SPA — a blank
//     page, not a degraded one — and this platform serves several
//     content surfaces (Vite-built SPA, streamed previews, WOPI
//     co-authoring against a separate Collabora/OnlyOffice origin).
//     Report-only lets an operator watch the violation reports of
//     their own deployment before flipping SEDOC_CSP_ENFORCE=true.
//     The default policy assumes the documented topology: the built
//     SPA and the API are served from the SAME origin by the reverse
//     proxy, so 'self' covers both. Deployments that put the
//     collaboration WebSocket or an object store on a different origin
//     must extend connect-src / img-src before enforcing.
package middleware

import (
	"net/http"
	"strconv"
	"strings"
)

// DefaultCSP is the starting policy for the SeDoc SPA + same-origin
// API. Directive-by-directive:
//
//	default-src 'self'      everything not named below is same-origin only
//	base-uri 'self'         a stored/injected <base> cannot repoint relative URLs
//	object-src 'none'       no Flash/Java/<object> plugin surface
//	frame-ancestors 'none'  nobody may frame us (clickjacking)
//	form-action 'self'      an injected form cannot POST credentials off-origin
//	script-src 'self'       Vite's production build emits external module
//	                        files, so no 'unsafe-inline' / 'unsafe-eval' needed
//	style-src ... 'unsafe-inline'
//	                        Radix + the chart/animation components write inline
//	                        style attributes; removing this needs per-element
//	                        nonces the SPA does not have today
//	img-src 'self' data: blob:
//	                        data: for inline icons, blob: for client-side
//	                        rendered page thumbnails
//	font-src 'self' data:   Tailwind ships fonts locally
//	connect-src 'self'      XHR/fetch/WebSocket to our own origin. 'self'
//	                        also authorises ws:// and wss:// on the same
//	                        origin, which is how /yjs/* is proxied
//	media-src 'self' blob:  audio/video preview
//	worker-src 'self' blob: PDF and OCR helpers run in blob workers
//	frame-src 'self' https: the co-authoring editor is framed from the
//	                        Collabora/OnlyOffice origin, which is
//	                        deployment-specific; narrow this to that exact
//	                        origin before enforcing
const DefaultCSP = "default-src 'self'; " +
	"base-uri 'self'; " +
	"object-src 'none'; " +
	"frame-ancestors 'none'; " +
	"form-action 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; " +
	"font-src 'self' data:; " +
	"connect-src 'self'; " +
	"media-src 'self' blob:; " +
	"worker-src 'self' blob:; " +
	"frame-src 'self' https:"

// Default header values. Exported so pkg/config can register them as
// viper defaults without the two packages drifting apart.
const (
	DefaultReferrerPolicy    = "no-referrer"
	DefaultPermissionsPolicy = "camera=(), microphone=(), geolocation=()"
	DefaultFrameOptions      = "DENY"

	// DefaultHSTSMaxAge is two years in seconds, the value the HSTS
	// preload list requires. Only ever emitted on an HTTPS request.
	DefaultHSTSMaxAge = 63072000
)

// SecurityHeadersConfig is the full knob set. The zero value emits
// nothing (Enabled defaults to false), so callers must opt in
// explicitly — either with DefaultSecurityHeadersConfig or from
// pkg/config, which is what the services do.
type SecurityHeadersConfig struct {
	// Enabled turns the whole middleware off when false. Present so a
	// deployment debugging a header conflict at the reverse proxy can
	// disable the service-side copy without a rebuild.
	Enabled bool

	// ContentSecurityPolicy is the policy string. Empty disables the
	// CSP headers entirely (the other headers still apply).
	ContentSecurityPolicy string

	// CSPEnforce sends the policy as `Content-Security-Policy`
	// (blocking) instead of `Content-Security-Policy-Report-Only`
	// (observing). False by default — see the package doc.
	CSPEnforce bool

	// CSPReportURI, when set, is appended to the policy as
	// `report-uri`. Optional; browsers simply log to the console when
	// no collector is configured.
	CSPReportURI string

	// FrameOptions is the X-Frame-Options value ("DENY" or
	// "SAMEORIGIN"). Empty omits the header.
	FrameOptions string

	// ReferrerPolicy / PermissionsPolicy are emitted verbatim. Empty
	// omits the header.
	ReferrerPolicy    string
	PermissionsPolicy string

	// HSTSMaxAgeSeconds <= 0 disables Strict-Transport-Security
	// completely. Any positive value is still only emitted on requests
	// that arrived over HTTPS.
	HSTSMaxAgeSeconds     int
	HSTSIncludeSubdomains bool
	HSTSPreload           bool

	// TrustForwardedProto lets an X-Forwarded-Proto: https header
	// satisfy the HTTPS test. Correct behind a reverse proxy that sets
	// the header itself (Kong, Caddy and ingress-nginx all overwrite
	// any client-supplied value). Set false for a service that
	// terminates TLS itself, so only a real r.TLS counts.
	TrustForwardedProto bool
}

// DefaultSecurityHeadersConfig returns the recommended settings:
// every header on, CSP in report-only mode, HSTS armed but
// HTTPS-conditional.
func DefaultSecurityHeadersConfig() SecurityHeadersConfig {
	return SecurityHeadersConfig{
		Enabled:               true,
		ContentSecurityPolicy: DefaultCSP,
		CSPEnforce:            false,
		FrameOptions:          DefaultFrameOptions,
		ReferrerPolicy:        DefaultReferrerPolicy,
		PermissionsPolicy:     DefaultPermissionsPolicy,
		HSTSMaxAgeSeconds:     DefaultHSTSMaxAge,
		HSTSIncludeSubdomains: true,
		HSTSPreload:           false,
		TrustForwardedProto:   true,
	}
}

// SecurityHeaders returns middleware that stamps the configured
// headers on every response. Header values are computed once at
// construction, not per request.
//
// Wrap it outermost:
//
//	httpSrv := &http.Server{
//	    Handler: middleware.SecurityHeaders(cfg)(
//	        middleware.RequireGatewaySignature()(root)),
//	}
func SecurityHeaders(cfg SecurityHeadersConfig) func(http.Handler) http.Handler {
	if !cfg.Enabled {
		return func(next http.Handler) http.Handler { return next }
	}

	policy := strings.TrimSpace(cfg.ContentSecurityPolicy)
	if policy != "" && cfg.CSPReportURI != "" {
		policy = strings.TrimSuffix(policy, ";") + "; report-uri " + cfg.CSPReportURI
	}

	// cspHeader is the header name the full policy goes out under.
	// enforcedFrameCSP is the small always-enforcing companion policy
	// used while the full one is report-only, because frame-ancestors
	// has no effect in a Report-Only policy.
	cspHeader, enforcedFrameCSP := "Content-Security-Policy", ""
	if policy != "" && !cfg.CSPEnforce {
		cspHeader = "Content-Security-Policy-Report-Only"
		if fa := frameAncestorsOf(policy); fa != "" {
			enforcedFrameCSP = fa
		}
	}

	hsts := hstsValue(cfg)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			if cfg.FrameOptions != "" {
				h.Set("X-Frame-Options", cfg.FrameOptions)
			}
			if cfg.ReferrerPolicy != "" {
				h.Set("Referrer-Policy", cfg.ReferrerPolicy)
			}
			if cfg.PermissionsPolicy != "" {
				h.Set("Permissions-Policy", cfg.PermissionsPolicy)
			}
			if policy != "" {
				h.Set(cspHeader, policy)
				if enforcedFrameCSP != "" {
					h.Set("Content-Security-Policy", enforcedFrameCSP)
				}
			}
			// Never on plain HTTP: a browser that caches HSTS for a host
			// with no TLS listener cannot reach it again until max-age
			// expires.
			if hsts != "" && requestIsHTTPS(r, cfg.TrustForwardedProto) {
				h.Set("Strict-Transport-Security", hsts)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// requestIsHTTPS reports whether the request reached the edge over
// TLS. r.TLS is set only when this process terminated TLS itself;
// behind a reverse proxy the answer comes from X-Forwarded-Proto,
// which may be a comma-separated chain ("https, http") whose FIRST
// element is the original client-facing scheme.
func requestIsHTTPS(r *http.Request, trustForwardedProto bool) bool {
	if r.TLS != nil {
		return true
	}
	if !trustForwardedProto {
		return false
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		return false
	}
	if i := strings.IndexByte(proto, ','); i >= 0 {
		proto = proto[:i]
	}
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

// hstsValue renders the Strict-Transport-Security value, or "" when
// HSTS is disabled.
func hstsValue(cfg SecurityHeadersConfig) string {
	if cfg.HSTSMaxAgeSeconds <= 0 {
		return ""
	}
	v := "max-age=" + strconv.Itoa(cfg.HSTSMaxAgeSeconds)
	if cfg.HSTSIncludeSubdomains {
		v += "; includeSubDomains"
	}
	// preload is only honoured with includeSubDomains, and is a
	// near-irreversible commitment; require both to be asked for.
	if cfg.HSTSPreload && cfg.HSTSIncludeSubdomains {
		v += "; preload"
	}
	return v
}

// frameAncestorsOf extracts a `frame-ancestors ...` directive from a
// policy so it can be re-sent in an enforcing header while the rest of
// the policy is still report-only. Returns "" when the policy has no
// such directive.
func frameAncestorsOf(policy string) string {
	for _, d := range strings.Split(policy, ";") {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		name, _, _ := strings.Cut(d, " ")
		if strings.EqualFold(name, "frame-ancestors") {
			return d
		}
	}
	return ""
}
