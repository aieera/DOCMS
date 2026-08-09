package middleware

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aieera/sedoc/pkg/config"
)

func secHeadersOKHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// serve runs one request through the middleware and returns the recorder.
func secHeadersServe(t *testing.T, cfg SecurityHeadersConfig, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/documents", nil)
	if mutate != nil {
		mutate(req)
	}
	rr := httptest.NewRecorder()
	SecurityHeaders(cfg)(secHeadersOKHandler()).ServeHTTP(rr, req)
	return rr
}

func TestSecurityHeaders_DefaultsOnPlainHTTP(t *testing.T) {
	rr := secHeadersServe(t, DefaultSecurityHeadersConfig(), nil)

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
		"Permissions-Policy":     "camera=(), microphone=(), geolocation=()",
	}
	for h, v := range want {
		if got := rr.Header().Get(h); got != v {
			t.Errorf("%s = %q, want %q", h, got, v)
		}
	}

	// The whole policy observes; only frame-ancestors blocks.
	if got := rr.Header().Get("Content-Security-Policy-Report-Only"); got != DefaultCSP {
		t.Errorf("report-only CSP = %q, want the default policy", got)
	}
	if got := rr.Header().Get("Content-Security-Policy"); got != "frame-ancestors 'none'" {
		t.Errorf("enforcing CSP = %q, want the frame-ancestors-only companion", got)
	}
}

// The whole point of the conditional: an HSTS header on a plain-HTTP
// deployment locks every browser out of the host for max-age seconds.
func TestSecurityHeaders_NoHSTSOverPlainHTTP(t *testing.T) {
	rr := secHeadersServe(t, DefaultSecurityHeadersConfig(), nil)
	if got := rr.Header().Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("HSTS must not be sent over plain HTTP, got %q", got)
	}
}

func TestSecurityHeaders_HSTSOnTLSRequest(t *testing.T) {
	rr := secHeadersServe(t, DefaultSecurityHeadersConfig(), func(r *http.Request) {
		r.TLS = &tls.ConnectionState{}
	})
	want := "max-age=63072000; includeSubDomains"
	if got := rr.Header().Get("Strict-Transport-Security"); got != want {
		t.Fatalf("HSTS = %q, want %q", got, want)
	}
}

func TestSecurityHeaders_HSTSFromForwardedProto(t *testing.T) {
	cfg := DefaultSecurityHeadersConfig()

	// Behind Kong/Caddy the service only ever sees plain HTTP; the
	// original scheme arrives in X-Forwarded-Proto, possibly as a chain.
	for _, proto := range []string{"https", "HTTPS", "https, http"} {
		rr := secHeadersServe(t, cfg, func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", proto) })
		if got := rr.Header().Get("Strict-Transport-Security"); got == "" {
			t.Errorf("X-Forwarded-Proto=%q should have produced HSTS", proto)
		}
	}
	for _, proto := range []string{"http", "", "http, https"} {
		rr := secHeadersServe(t, cfg, func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", proto) })
		if got := rr.Header().Get("Strict-Transport-Security"); got != "" {
			t.Errorf("X-Forwarded-Proto=%q must not produce HSTS, got %q", proto, got)
		}
	}

	// A service terminating its own TLS must ignore the header entirely.
	cfg.TrustForwardedProto = false
	rr := secHeadersServe(t, cfg, func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "https") })
	if got := rr.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("TrustForwardedProto=false must ignore the header, got %q", got)
	}
}

func TestSecurityHeaders_PreloadRequiresIncludeSubdomains(t *testing.T) {
	cfg := DefaultSecurityHeadersConfig()
	cfg.HSTSPreload = true
	cfg.HSTSIncludeSubdomains = false
	rr := secHeadersServe(t, cfg, func(r *http.Request) { r.TLS = &tls.ConnectionState{} })
	if got := rr.Header().Get("Strict-Transport-Security"); strings.Contains(got, "preload") {
		t.Fatalf("preload without includeSubDomains is invalid, got %q", got)
	}
}

func TestSecurityHeaders_EnforcingCSPUsesSingleHeader(t *testing.T) {
	cfg := DefaultSecurityHeadersConfig()
	cfg.CSPEnforce = true
	rr := secHeadersServe(t, cfg, nil)

	if got := rr.Header().Get("Content-Security-Policy"); got != DefaultCSP {
		t.Errorf("enforcing CSP = %q, want the full policy", got)
	}
	if got := rr.Header().Get("Content-Security-Policy-Report-Only"); got != "" {
		t.Errorf("report-only header must not be sent when enforcing, got %q", got)
	}
}

func TestSecurityHeaders_ReportURIAppended(t *testing.T) {
	cfg := DefaultSecurityHeadersConfig()
	cfg.CSPReportURI = "/api/v1/csp-reports"
	rr := secHeadersServe(t, cfg, nil)
	got := rr.Header().Get("Content-Security-Policy-Report-Only")
	if !strings.HasSuffix(got, "; report-uri /api/v1/csp-reports") {
		t.Fatalf("report-uri not appended: %q", got)
	}
}

func TestSecurityHeaders_DisabledIsPassthrough(t *testing.T) {
	cfg := DefaultSecurityHeadersConfig()
	cfg.Enabled = false
	rr := secHeadersServe(t, cfg, nil)
	for _, h := range []string{
		"X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy",
		"Permissions-Policy", "Content-Security-Policy",
		"Content-Security-Policy-Report-Only",
	} {
		if got := rr.Header().Get(h); got != "" {
			t.Errorf("disabled middleware still set %s = %q", h, got)
		}
	}
	if rr.Code != http.StatusOK {
		t.Errorf("handler must still run, got %d", rr.Code)
	}
}

func TestSecurityHeaders_EmptyValuesOmitHeaders(t *testing.T) {
	rr := secHeadersServe(t, SecurityHeadersConfig{Enabled: true}, nil)
	// nosniff is unconditional; everything else was left empty.
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("nosniff is unconditional, got %q", got)
	}
	for _, h := range []string{
		"X-Frame-Options", "Referrer-Policy", "Permissions-Policy",
		"Content-Security-Policy", "Content-Security-Policy-Report-Only",
	} {
		if got := rr.Header().Get(h); got != "" {
			t.Errorf("%s should be omitted when unset, got %q", h, got)
		}
	}
}

// A handler that deliberately serves framed content (the IRM protected
// stream) must be able to relax the frame headers on its own response.
func TestSecurityHeaders_HandlerCanOverride(t *testing.T) {
	h := SecurityHeaders(DefaultSecurityHeadersConfig())(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Frame-Options", "SAMEORIGIN")
			w.Header().Set("Content-Security-Policy", "sandbox; frame-ancestors 'self'")
			w.WriteHeader(http.StatusOK)
		}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/irm/licenses/content", nil))

	if got := rr.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Errorf("handler override lost: X-Frame-Options = %q", got)
	}
	if got := rr.Header().Get("Content-Security-Policy"); got != "sandbox; frame-ancestors 'self'" {
		t.Errorf("handler override lost: CSP = %q", got)
	}
}

// The default policy must not contain the two directives that would
// make a strict CSP pointless, and must keep passkeys usable.
func TestDefaultCSP_Shape(t *testing.T) {
	for _, banned := range []string{"'unsafe-eval'", "script-src 'self' 'unsafe-inline'"} {
		if strings.Contains(DefaultCSP, banned) {
			t.Errorf("default CSP must not contain %s", banned)
		}
	}
	for _, required := range []string{
		"default-src 'self'", "frame-ancestors 'none'", "object-src 'none'",
		"base-uri 'self'", "form-action 'self'",
	} {
		if !strings.Contains(DefaultCSP, required) {
			t.Errorf("default CSP missing %s", required)
		}
	}
	// Naming publickey-credentials-* with an empty allowlist kills
	// WebAuthn; the default policy must stay silent about them.
	if strings.Contains(DefaultPermissionsPolicy, "publickey-credentials") {
		t.Error("Permissions-Policy must not mention publickey-credentials-* — it would disable passkeys")
	}
}

func TestFrameAncestorsOf(t *testing.T) {
	cases := map[string]string{
		"default-src 'self'; frame-ancestors 'none'; img-src *": "frame-ancestors 'none'",
		"frame-ancestors 'self' https://a.example":              "frame-ancestors 'self' https://a.example",
		"default-src 'self'":                                    "",
		"":                                                      "",
	}
	for policy, want := range cases {
		if got := frameAncestorsOf(policy); got != want {
			t.Errorf("frameAncestorsOf(%q) = %q, want %q", policy, got, want)
		}
	}
}

// ---- config bridge ---------------------------------------------------

func TestSecurityHeadersFromConfig_Defaults(t *testing.T) {
	// Mirrors what config.Load produces with no SEDOC_* overrides.
	cfg := &config.Config{
		SecurityHeadersEnabled: true,
		HSTSEnabled:            true,
		HSTSIncludeSubdomains:  true,
		TrustForwardedProto:    true,
	}
	got := SecurityHeadersFromConfig(cfg)
	want := DefaultSecurityHeadersConfig()
	if got != want {
		t.Fatalf("blank config should reproduce the defaults\n got: %+v\nwant: %+v", got, want)
	}
}

func TestSecurityHeadersFromConfig_Overrides(t *testing.T) {
	cfg := &config.Config{
		SecurityHeadersEnabled: true,
		ContentSecurityPolicy:  "default-src 'none'",
		CSPEnforce:             true,
		FrameOptions:           "SAMEORIGIN",
		ReferrerPolicy:         "strict-origin-when-cross-origin",
		HSTSEnabled:            true,
		HSTSMaxAgeSeconds:      300,
		HSTSIncludeSubdomains:  false,
	}
	got := SecurityHeadersFromConfig(cfg)
	if got.ContentSecurityPolicy != "default-src 'none'" || !got.CSPEnforce {
		t.Errorf("CSP override not applied: %+v", got)
	}
	if got.FrameOptions != "SAMEORIGIN" || got.ReferrerPolicy != "strict-origin-when-cross-origin" {
		t.Errorf("header overrides not applied: %+v", got)
	}
	if got.HSTSMaxAgeSeconds != 300 {
		t.Errorf("HSTSMaxAgeSeconds = %d, want 300", got.HSTSMaxAgeSeconds)
	}
	// PermissionsPolicy was left blank → built-in default.
	if got.PermissionsPolicy != DefaultPermissionsPolicy {
		t.Errorf("blank knob should fall back to the default, got %q", got.PermissionsPolicy)
	}
}

func TestSecurityHeadersFromConfig_OffDisablesOneHeader(t *testing.T) {
	cfg := &config.Config{
		SecurityHeadersEnabled: true,
		ContentSecurityPolicy:  "off",
		FrameOptions:           "OFF",
		HSTSEnabled:            false,
	}
	got := SecurityHeadersFromConfig(cfg)
	if got.ContentSecurityPolicy != "" {
		t.Errorf(`"off" should disable the CSP, got %q`, got.ContentSecurityPolicy)
	}
	if got.FrameOptions != "" {
		t.Errorf(`"OFF" should be case-insensitive, got %q`, got.FrameOptions)
	}
	if got.HSTSMaxAgeSeconds != 0 {
		t.Errorf("HSTSEnabled=false must zero the max-age, got %d", got.HSTSMaxAgeSeconds)
	}
	// Disabling one header must not disable the rest.
	if got.ReferrerPolicy != DefaultReferrerPolicy {
		t.Errorf("ReferrerPolicy = %q, want the default", got.ReferrerPolicy)
	}
}

func TestSecurityHeadersFromConfig_NilIsSafe(t *testing.T) {
	if got := SecurityHeadersFromConfig(nil); got != DefaultSecurityHeadersConfig() {
		t.Fatalf("nil config should yield the defaults, got %+v", got)
	}
}
