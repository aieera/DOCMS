// Package netguard builds outbound HTTP clients for URLs that come from
// untrusted input (DEC-18): a certificate's OCSP responder or CRL
// distribution point, a webhook target, a link inside an uploaded file.
//
// NewClient refuses, at dial time, every destination in loopback, private
// (RFC 1918 / ULA), link-local, CGNAT, multicast and other non-routable
// ranges, including IPv4-mapped and NAT64 forms of them. The check runs on
// the address the socket actually connects to, after DNS resolution, so a
// hostname that resolves (or re-resolves) to an internal address is refused
// too. Environment proxies are ignored because a proxy would move the dial to
// the proxy's address. Redirects are capped at MaxRedirects and only http and
// https are followed.
//
// LimitedBody reads a response body with a hard size cap.
//
// The package uses only the standard library.
package netguard

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// DefaultTimeout is the whole-request timeout used when Options.Timeout is zero.
const DefaultTimeout = 10 * time.Second

// MaxRedirects is the number of redirects a client follows before giving up.
const MaxRedirects = 3

// Test hooks; nil in production.
var (
	dialExempt   func(netip.AddrPort) bool // lets a test stand a loopback server in for a public host
	testResolver *net.Resolver             // lets a test answer DNS
)

// ErrBlockedAddress is returned (wrapped) when a dial targets a refused address.
var ErrBlockedAddress = errors.New("netguard: destination address not allowed")

// ErrBodyTooLarge is returned by LimitedBody when the body exceeds the cap.
var ErrBodyTooLarge = errors.New("netguard: body exceeds size limit")

// Options configures NewClient.
type Options struct {
	// Timeout bounds the whole request including reading the body.
	// Zero means DefaultTimeout.
	Timeout time.Duration
	// AllowPrivate disables the address deny-list. Only for trusted,
	// operator-configured destinations and for tests.
	AllowPrivate bool
}

// NewClient returns an *http.Client that enforces the address deny-list at
// dial time (unless opts.AllowPrivate), follows at most MaxRedirects http(s)
// redirects and times out after opts.Timeout (default DefaultTimeout).
func NewClient(opts Options) *http.Client {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	if !opts.AllowPrivate {
		dialer.Control = guardControl
	}
	if testResolver != nil {
		dialer.Resolver = testResolver
	}
	transport := &http.Transport{
		Proxy:                 nil, // never route through an environment proxy
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{
		Timeout:       timeout,
		Transport:     transport,
		CheckRedirect: checkRedirect,
	}
}

func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > MaxRedirects {
		return fmt.Errorf("netguard: stopped after %d redirects", MaxRedirects)
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return fmt.Errorf("netguard: redirect to scheme %q refused", req.URL.Scheme)
	}
	return nil
}

// guardControl runs after DNS resolution with the literal address being
// connected to, so it sees exactly where the socket goes.
func guardControl(_ string, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: unparseable dial address %q", ErrBlockedAddress, address)
	}
	if dialExempt != nil && dialExempt(ap) {
		return nil
	}
	if Blocked(ap.Addr()) {
		return fmt.Errorf("%w: %s", ErrBlockedAddress, ap.Addr())
	}
	return nil
}

// deniedV4 lists the IPv4 ranges NewClient refuses beyond what the netip
// predicates already cover.
var deniedV4 = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),   // CGNAT (RFC 6598)
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1
	netip.MustParsePrefix("192.88.99.0/24"),  // 6to4 relay anycast
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved + broadcast
}

var (
	ipv4Compat   = netip.MustParsePrefix("::/96") // deprecated IPv4-compatible (incl. :: and ::1)
	nat64Prefix  = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour    = netip.MustParsePrefix("2002::/16")
	siteLocalV6  = netip.MustParsePrefix("fec0::/10")
	discardV6    = netip.MustParsePrefix("100::/64")
	documentV6   = netip.MustParsePrefix("2001:db8::/32")
	teredoPrefix = netip.MustParsePrefix("2001::/32")
)

// Blocked reports whether a dial to addr is refused: loopback, private
// (RFC 1918, fc00::/7), link-local, CGNAT, multicast, unspecified and other
// non-routable ranges, and IPv6 forms that embed such an IPv4 address
// (IPv4-mapped, NAT64, 6to4). Teredo, the 6to4 relay anycast range and
// the deprecated IPv4-compatible ::/96 are refused outright.
func Blocked(addr netip.Addr) bool {
	if !addr.IsValid() {
		return true
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return blockedV4(addr)
	}
	if addr.IsLoopback() || addr.IsUnspecified() || addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() || addr.IsMulticast() ||
		addr.IsLinkLocalMulticast() || addr.IsInterfaceLocalMulticast() {
		return true
	}
	if ipv4Compat.Contains(addr) || siteLocalV6.Contains(addr) || discardV6.Contains(addr) || documentV6.Contains(addr) || teredoPrefix.Contains(addr) {
		return true
	}
	b := addr.As16()
	if nat64Prefix.Contains(addr) {
		return blockedV4(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
	}
	if sixToFour.Contains(addr) {
		return blockedV4(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}))
	}
	return false
}

func blockedV4(addr netip.Addr) bool {
	if addr.IsLoopback() || addr.IsUnspecified() || addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() || addr.IsMulticast() || addr.IsLinkLocalMulticast() {
		return true
	}
	for _, p := range deniedV4 {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// LimitedBody reads r to the end and returns its bytes, or ErrBodyTooLarge
// (wrapped) as soon as more than max bytes arrive. max must be positive.
func LimitedBody(r io.Reader, max int64) ([]byte, error) {
	if max <= 0 {
		return nil, fmt.Errorf("netguard: body limit must be positive, got %d", max)
	}
	body, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > max {
		return nil, fmt.Errorf("%w: more than %d bytes", ErrBodyTooLarge, max)
	}
	return body, nil
}
