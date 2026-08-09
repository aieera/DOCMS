package middleware

import (
	"strings"

	"github.com/aieera/sedoc/pkg/config"
)

// disableToken is the value an operator sets on a string knob to turn
// that single header off (e.g. SEDOC_FRAME_OPTIONS=off). An empty
// value means "use the built-in default" instead, so there has to be
// an explicit way to say "emit nothing".
const disableToken = "off"

// SecurityHeadersFromConfig builds the middleware settings from the
// service configuration, filling in the package defaults for every
// knob the operator left blank.
//
// This is the only place the two packages meet: pkg/config owns the
// env-var plumbing and the on/off switches, pkg/middleware owns the
// actual header values. Services wire it in one line:
//
//	Handler: middleware.SecurityHeaders(middleware.SecurityHeadersFromConfig(cfg))(root)
func SecurityHeadersFromConfig(cfg *config.Config) SecurityHeadersConfig {
	out := DefaultSecurityHeadersConfig()
	if cfg == nil {
		return out
	}

	out.Enabled = cfg.SecurityHeadersEnabled
	out.CSPEnforce = cfg.CSPEnforce
	out.CSPReportURI = strings.TrimSpace(cfg.CSPReportURI)
	out.ContentSecurityPolicy = orDefault(cfg.ContentSecurityPolicy, DefaultCSP)
	out.FrameOptions = orDefault(cfg.FrameOptions, DefaultFrameOptions)
	out.ReferrerPolicy = orDefault(cfg.ReferrerPolicy, DefaultReferrerPolicy)
	out.PermissionsPolicy = orDefault(cfg.PermissionsPolicy, DefaultPermissionsPolicy)
	out.TrustForwardedProto = cfg.TrustForwardedProto

	// HSTS: the switch and the value are separate knobs so that
	// "disabled" and "left at the default max-age" are distinguishable.
	switch {
	case !cfg.HSTSEnabled:
		out.HSTSMaxAgeSeconds = 0
	case cfg.HSTSMaxAgeSeconds > 0:
		out.HSTSMaxAgeSeconds = cfg.HSTSMaxAgeSeconds
	default:
		out.HSTSMaxAgeSeconds = DefaultHSTSMaxAge
	}
	out.HSTSIncludeSubdomains = cfg.HSTSIncludeSubdomains
	out.HSTSPreload = cfg.HSTSPreload

	return out
}

// orDefault resolves the empty="use default", "off"="emit nothing"
// convention documented on the config fields.
func orDefault(v, def string) string {
	v = strings.TrimSpace(v)
	switch {
	case v == "":
		return def
	case strings.EqualFold(v, disableToken):
		return ""
	default:
		return v
	}
}
