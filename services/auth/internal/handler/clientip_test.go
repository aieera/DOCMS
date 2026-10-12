package handler

import (
	"net/http/httptest"
	"testing"
)

// clientIP always yields a bare IP literal (or ""), never a hop list or a
// bracketed IPv6 host, so it is safe for the INET columns (L-3).
func TestClientIP(t *testing.T) {
	cases := []struct {
		name, xff, remote, want string
	}{
		{"first hop of a list", "203.0.113.9, 10.1.2.3, 172.16.0.1", "10.0.0.1:443", "203.0.113.9"},
		{"single hop", "198.51.100.7", "10.0.0.1:443", "198.51.100.7"},
		{"garbage xff falls back", "unknown, 10.1.2.3", "192.0.2.4:5555", "192.0.2.4"},
		{"no xff", "", "192.0.2.5:1234", "192.0.2.5"},
		{"ipv6 remote unbracketed", "", "[2001:db8::1]:443", "2001:db8::1"},
		{"ipv6 xff", "2001:db8::2, 10.0.0.1", "10.0.0.1:443", "2001:db8::2"},
		{"unparseable remote", "", "pipe", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
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
