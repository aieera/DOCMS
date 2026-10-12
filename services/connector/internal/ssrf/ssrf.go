// Package ssrf holds the connector's outbound-connection guard: the address
// classes a tenant-chosen endpoint (webhook URL, IMAP server) must never
// reach, and a dialer Control hook that enforces it on the address actually
// dialed after DNS resolution (so DNS rebinding cannot bypass a
// create-time check).
package ssrf

import (
	"fmt"
	"net"
	"syscall"
)

// IsInternalIP reports whether ip is loopback, RFC 1918 / ULA private,
// link-local (unicast + multicast), unspecified, any multicast, 0.0.0.0/8,
// or CGNAT 100.64.0.0/10 (RFC 6598, not covered by net.IP.IsPrivate).
func IsInternalIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 0 {
			return true
		}
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return true
		}
	}
	return false
}

// DialControl is a net.Dialer.Control hook that refuses internal addresses.
func DialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || IsInternalIP(ip) {
		return fmt.Errorf("blocked connection to internal address %q (SSRF guard)", address)
	}
	return nil
}
