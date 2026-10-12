package handler

import (
	"net/http/httptest"
	"testing"
)

// clientIP is the shared trusted-proxy resolver (DEC-5): X-Forwarded-For
// counts only from a peer in SEDOC_TRUSTED_PROXIES, and then only its
// right-most untrusted hop. The result is always a bare IP literal (or ""),
// never a hop list or a bracketed IPv6 host, so it is safe for the INET
// columns (L-3).
func TestClientIP(t *testing.T) {
	cases := []struct {
		name, trusted, xff, remote, want string
	}{
		{"untrusted peer: xff ignored", "", "203.0.113.9, 10.1.2.3", "192.0.2.7:443", "192.0.2.7"},
		{"untrusted peer: spoofed single hop ignored", "10.0.0.0/8", "198.51.100.7", "192.0.2.8:443", "192.0.2.8"},
		{"trusted peer: right-most untrusted hop", "10.0.0.0/8", "203.0.113.9, 10.1.2.3", "10.0.0.1:443", "203.0.113.9"},
		{"trusted peer: client-prepended spoof ignored", "10.0.0.0/8", "198.51.100.66, 203.0.113.9, 10.1.2.3", "10.0.0.1:443", "203.0.113.9"},
		{"trusted peer: ipv6 client", "10.0.0.0/8", "2001:db8::2", "10.0.0.1:443", "2001:db8::2"},
		{"no xff", "10.0.0.0/8", "", "192.0.2.5:1234", "192.0.2.5"},
		{"ipv6 remote unbracketed", "", "", "[2001:db8::1]:443", "2001:db8::1"},
		{"unparseable remote", "", "", "pipe", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("SEDOC_TRUSTED_PROXIES", c.trusted)
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = c.remote
			if c.xff != "" {
				r.Header.Set("X-Forwarded-For", c.xff)
			}
			if got := clientIP(r); got != c.want {
				t.Fatalf("clientIP = %q, want %q", got, c.want)
			}
		})
	}
}
