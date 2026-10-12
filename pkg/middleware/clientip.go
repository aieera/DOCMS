package middleware

import (
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
)

// TrustedProxiesEnv names the comma-separated list of CIDRs (bare IPs are
// accepted as single-host prefixes) whose X-Forwarded-For entries
// ClientIP believes — the gateway / load-balancer tier (DEC-5). Empty
// (the default) trusts no proxy: X-Forwarded-For is ignored and the
// client IP is the TCP peer.
const TrustedProxiesEnv = "SEDOC_TRUSTED_PROXIES"

// ClientIP resolves the caller's IP for audit rows, request logs and rate
// limiting (DEC-5, R-1/R-2).
//
// If the direct peer (RemoteAddr) is a trusted proxy, the answer is the
// right-most X-Forwarded-For hop that is not itself a trusted proxy: each
// proxy APPENDS the address it received the connection from, so that hop
// was written by our own infrastructure, while everything to its left is
// whatever the client chose to send. Otherwise — and whenever no such hop
// exists or it is not a valid IP — the answer is RemoteAddr's host.
// X-Real-IP and the left-most X-Forwarded-For entry are never trusted.
//
// The result is always a bare IP (no port, no zone) or "" — it lands in
// Postgres inet columns, which reject anything else.
func ClientIP(r *http.Request) string {
	remote := remoteHost(r.RemoteAddr)
	trusted := trustedProxies()
	if len(trusted) == 0 || !trusted.contains(remote) {
		return remote
	}
	hops := forwardedHops(r.Header.Values("X-Forwarded-For"))
	for i := len(hops) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(hops[i])
		if err != nil {
			// Not an address our proxy would have written — the chain is
			// malformed; don't let it pick the identity.
			return remote
		}
		if !trusted.containsAddr(addr) {
			return addr.WithZone("").Unmap().String()
		}
	}
	return remote
}

// remoteHost reduces RemoteAddr to a bare IP ("" when it is not one, e.g.
// a unix-socket peer).
func remoteHost(remoteAddr string) string {
	host := strings.Trim(remoteAddr, "[]")
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return ""
	}
	return addr.WithZone("").Unmap().String()
}

// forwardedHops flattens every X-Forwarded-For header line, in order,
// into individual trimmed entries.
func forwardedHops(lines []string) []string {
	var out []string
	for _, line := range lines {
		for _, hop := range strings.Split(line, ",") {
			if hop = strings.TrimSpace(hop); hop != "" {
				out = append(out, hop)
			}
		}
	}
	return out
}

type prefixSet []netip.Prefix

func (s prefixSet) contains(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	return s.containsAddr(addr)
}

func (s prefixSet) containsAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, p := range s {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// trustedProxies parses TrustedProxiesEnv, re-parsing only when the raw
// value changes (cheap per request, and tests can t.Setenv freely).
// Unparseable entries are skipped.
func trustedProxies() prefixSet {
	raw := os.Getenv(TrustedProxiesEnv)
	trustedCache.mu.Lock()
	defer trustedCache.mu.Unlock()
	if trustedCache.parsed && trustedCache.raw == raw {
		return trustedCache.set
	}
	var set prefixSet
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if p, err := netip.ParsePrefix(part); err == nil {
			set = append(set, p.Masked())
			continue
		}
		if a, err := netip.ParseAddr(part); err == nil {
			a = a.Unmap()
			set = append(set, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	trustedCache.raw, trustedCache.set, trustedCache.parsed = raw, set, true
	return set
}

var trustedCache struct {
	mu     sync.Mutex
	parsed bool
	raw    string
	set    prefixSet
}
