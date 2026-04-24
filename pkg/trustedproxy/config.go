// Package trustedproxy centralises the "where does the real client IP
// come from" decision so every middleware that cares (geofence, rate
// limit, request log, per-service internal handlers) uses the same
// logic and reads the same config.
//
// The old pattern was a private realClientIP(r) in every middleware
// that took the right-most X-Forwarded-For hop. That trusts the
// right-most hop unconditionally, which is spoofable if the
// middleware is ever mounted without Kong in front (see T-D-2 in
// docs/tech-debt/ledger.md).
//
// The correct pattern: walk XFF right-to-left, skipping hops whose
// SOURCE (the previous hop or r.RemoteAddr at the first step) is in
// the trusted-proxy CIDR list. The first untrusted source is the real
// client. Outside the trusted chain, XFF is attacker-controlled and
// must not be believed.
package trustedproxy

import (
	"fmt"
	"net/netip"
	"os"
	"strings"
)

// EnvTrustedCIDRs is the env var that supplies the comma-separated
// list of CIDR prefixes considered trusted proxy hops.
const EnvTrustedCIDRs = "VAULTDMS_TRUSTED_PROXY_CIDRS"

// EnvAppEnv is the env var that signals the deploy environment;
// when set to "production", an empty CIDR list is fatal.
const EnvAppEnv = "VAULTDMS_ENV"

// Config holds the trusted-proxy CIDR allowlist.
type Config struct {
	TrustedCIDRs []netip.Prefix
}

// LoadFromEnv builds a Config from VAULTDMS_TRUSTED_PROXY_CIDRS.
// In production (VAULTDMS_ENV=production) an empty or unparseable
// list is fatal — the process panics so a misconfigured deploy fails
// loudly instead of silently trusting spoofed XFF headers.
func LoadFromEnv() Config {
	raw := os.Getenv(EnvTrustedCIDRs)
	cfg, err := Parse(raw)
	if err != nil {
		panic(fmt.Sprintf("trustedproxy: %s: %v", EnvTrustedCIDRs, err))
	}
	if len(cfg.TrustedCIDRs) == 0 && os.Getenv(EnvAppEnv) == "production" {
		panic("trustedproxy: " + EnvTrustedCIDRs + " must be set in production — refusing to trust client-supplied X-Forwarded-For headers")
	}
	return cfg
}

// Parse constructs a Config from a comma-separated CIDR list. Empty
// or whitespace-only input yields an empty Config (caller decides
// whether that's fatal — see LoadFromEnv for the production rule).
func Parse(raw string) (Config, error) {
	var cfg Config
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return cfg, nil
	}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		pfx, err := netip.ParsePrefix(part)
		if err != nil {
			return Config{}, fmt.Errorf("invalid CIDR %q: %w", part, err)
		}
		cfg.TrustedCIDRs = append(cfg.TrustedCIDRs, pfx)
	}
	return cfg, nil
}

// Trusts reports whether addr falls inside any trusted-proxy CIDR.
func (c Config) Trusts(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	// Normalise IPv4-in-IPv6 so a prefix declared as 10.0.0.0/8
	// matches even if the runtime gave us ::ffff:10.0.0.1.
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	for _, pfx := range c.TrustedCIDRs {
		if pfx.Contains(addr) {
			return true
		}
	}
	return false
}
