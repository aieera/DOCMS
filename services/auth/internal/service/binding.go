package service

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"strings"
)

// Session binding primitives per Blueprint §8.1.
//
// The session stores ip_address + user_agent at creation. On every
// authenticated request we compute the same two derivations from the
// current request and compare against the stored values. Mismatches are
// handled according to the tenant's binding-strictness setting.
//
// Why /24 for IPv4 and /56 for IPv6:
//   - /24 covers the common case of a client moving between IPs on the
//     same consumer/corporate NAT pool without forcing re-auth every
//     time the carrier re-leases. Tighter (/32) breaks mobile networks;
//     looser gives up enough subnet to be useless.
//   - /56 is the consumer IPv6 delegation size most ISPs hand out, so a
//     single home network stays on one prefix.

// BindingStrictness mirrors the organizations.session_binding_strictness
// CHECK constraint.
type BindingStrictness string

const (
	BindingNone    BindingStrictness = "none"
	BindingWarn    BindingStrictness = "warn"
	BindingEnforce BindingStrictness = "enforce"
)

// IPBindingCIDR returns the network portion of an IP per the binding
// policy: /24 for IPv4, /56 for IPv6. Returns "" when the input is not a
// parseable IP; callers treat that as "unknown" and never mismatch an
// empty value against a stored one.
func IPBindingCIDR(ipStr string) string {
	ipStr = strings.TrimSpace(ipStr)
	// The request IP may include a port ("1.2.3.4:5678") or be prefixed
	// with the port-less XFF form. Strip a trailing port if present.
	if host, _, err := net.SplitHostPort(ipStr); err == nil {
		ipStr = host
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return ""
	}
	var mask net.IPMask
	if v4 := ip.To4(); v4 != nil {
		ip = v4
		mask = net.CIDRMask(24, 32)
	} else {
		mask = net.CIDRMask(56, 128)
	}
	return (&net.IPNet{IP: ip.Mask(mask), Mask: mask}).String()
}

// UserAgentFingerprint returns a SHA-256 of the canonicalized UA. The
// canonical form collapses whitespace and lowercases the string, which
// absorbs the common trivial UA drift (e.g. Chrome rounding version
// numbers between minor updates) without losing enough signal to catch
// a token moved to an entirely different browser.
//
// Empty UA ⇒ empty fingerprint ⇒ never triggers a mismatch (same
// unknown-input handling as IPBindingCIDR).
func UserAgentFingerprint(ua string) string {
	ua = strings.ToLower(strings.Join(strings.Fields(ua), " "))
	if ua == "" {
		return ""
	}
	h := sha256.Sum256([]byte(ua))
	return hex.EncodeToString(h[:])
}

// BindingMatches returns true when the request's IP/UA are either the
// same as stored OR one side is "unknown" (empty). The unknown-input
// case exists because legacy sessions may not have had UA captured, and
// a test harness may not supply a real IP — we don't want to reject
// them outright.
func BindingMatches(storedIP, storedUA, reqIP, reqUA string) bool {
	storedCIDR := IPBindingCIDR(storedIP)
	reqCIDR := IPBindingCIDR(reqIP)
	if storedCIDR != "" && reqCIDR != "" && storedCIDR != reqCIDR {
		return false
	}
	storedFP := UserAgentFingerprint(storedUA)
	reqFP := UserAgentFingerprint(reqUA)
	if storedFP != "" && reqFP != "" && storedFP != reqFP {
		return false
	}
	return true
}
