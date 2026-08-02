package handler

import (
	"net/http"
	"testing"
)

// Regression: Postgres `inet` rejects host:port, so RemoteAddr must be
// reduced to a bare IP before it reaches the audit columns.
func TestClientIP(t *testing.T) {
	cases := []struct{ name, remote, xff, want string }{
		{"remote with port", "172.18.0.1:59222", "", "172.18.0.1"},
		{"ipv6 with port", "[2001:db8::1]:443", "", "2001:db8::1"},
		{"bare remote", "10.0.0.5", "", "10.0.0.5"},
		{"xff first hop wins", "172.18.0.1:59222", "203.0.113.9, 10.0.0.1", "203.0.113.9"},
		{"xff with spaces", "172.18.0.1:59222", "  203.0.113.9 ", "203.0.113.9"},
		{"xff with port", "172.18.0.1:59222", "203.0.113.9:1234", "203.0.113.9"},
		{"garbage xff falls back", "172.18.0.1:59222", "not-an-ip", "172.18.0.1"},
		{"unparseable everything", "garbage", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _ := http.NewRequest("POST", "/", nil)
			r.RemoteAddr = c.remote
			if c.xff != "" {
				r.Header.Set("X-Forwarded-For", c.xff)
			}
			if got := clientIP(r); got != c.want {
				t.Errorf("clientIP() = %q, want %q", got, c.want)
			}
		})
	}
}
