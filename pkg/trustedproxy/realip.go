package trustedproxy

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// RealClientIP returns the first untrusted hop in the chain
// [r.RemoteAddr, XFF[n], XFF[n-1], ..., XFF[0]].
//
// Walk order: start at r.RemoteAddr (the immediate peer). If that is
// trusted, step to the right-most XFF entry, then the next left, and
// so on. The first address whose PRIOR hop is trusted but itself is
// not is the real client. If the whole chain is trusted the left-most
// XFF entry is returned (best-guess client).
//
// Falls back to the parsed r.RemoteAddr when X-Forwarded-For is empty.
// Returns the zero netip.Addr if nothing parseable is present —
// callers should handle that as "unknown source".
func RealClientIP(r *http.Request, cfg Config) netip.Addr {
	peer := parseRemoteAddr(r.RemoteAddr)

	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return peer
	}

	hops := splitHops(xff)
	if len(hops) == 0 {
		return peer
	}

	// If the peer itself is not a trusted proxy, XFF is
	// attacker-controlled — return the peer address.
	if !peer.IsValid() || !cfg.Trusts(peer) {
		return peer
	}

	// Walk right-to-left. The hop that *sent* XFF[i] is XFF[i+1]
	// (or the peer, for the right-most). If that sender is
	// trusted, XFF[i] is believable; if XFF[i] itself is then
	// untrusted, it's the real client.
	for i := len(hops) - 1; i >= 0; i-- {
		hop := hops[i]
		if !hop.IsValid() {
			// Unparseable entry breaks the trust chain — fall
			// back to the last-known trusted hop's predecessor.
			if i+1 < len(hops) {
				return hops[i+1]
			}
			return peer
		}
		if !cfg.Trusts(hop) {
			return hop
		}
	}
	// Whole chain trusted: the left-most XFF entry is the best we
	// have for the client.
	return hops[0]
}

func parseRemoteAddr(remote string) netip.Addr {
	if remote == "" {
		return netip.Addr{}
	}
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	return addr
}

func splitHops(xff string) []netip.Addr {
	parts := strings.Split(xff, ",")
	out := make([]netip.Addr, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		addr, err := netip.ParseAddr(p)
		if err != nil {
			out = append(out, netip.Addr{})
			continue
		}
		if addr.Is4In6() {
			addr = addr.Unmap()
		}
		out = append(out, addr)
	}
	return out
}
