package netguard

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewClient_RefusesLoopbackAtDialTime(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	c := NewClient(Options{})
	resp, err := c.Get(srv.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected the loopback destination to be refused")
	}
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v, want ErrBlockedAddress", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("server was reached %d times; the dial must be refused", hits.Load())
	}
}

func TestNewClient_AllowPrivateReachesLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	resp, err := NewClient(Options{AllowPrivate: true}).Get(srv.URL)
	if err != nil {
		t.Fatalf("AllowPrivate client: %v", err)
	}
	_ = resp.Body.Close()
}

func TestNewClient_RedirectCap(t *testing.T) {
	var hits atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, srv.URL+"/next", http.StatusFound)
	}))
	defer srv.Close()

	resp, err := NewClient(Options{AllowPrivate: true}).Get(srv.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected the redirect chain to be cut")
	}
	// One original request + MaxRedirects followed redirects.
	if got := hits.Load(); got != MaxRedirects+1 {
		t.Fatalf("server hits = %d, want %d", got, MaxRedirects+1)
	}
}

func TestNewClient_DefaultTimeout(t *testing.T) {
	if got := NewClient(Options{}).Timeout; got != DefaultTimeout {
		t.Fatalf("Timeout = %v, want %v", got, DefaultTimeout)
	}
	if got := NewClient(Options{Timeout: 3 * time.Second}).Timeout; got != 3*time.Second {
		t.Fatalf("Timeout = %v, want 3s", got)
	}
}

func TestNewClient_IgnoresEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	tr, ok := NewClient(Options{}).Transport.(*http.Transport)
	if !ok {
		t.Fatal("transport is not *http.Transport")
	}
	if tr.Proxy != nil {
		t.Fatal("a proxy would move the dial to the proxy address and bypass the guard")
	}
}

func TestBlocked(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1":          true,
		"127.255.0.9":        true,
		"10.1.2.3":           true,
		"172.16.0.1":         true,
		"172.31.255.255":     true,
		"192.168.10.1":       true,
		"169.254.169.254":    true, // cloud metadata
		"100.64.0.1":         true, // CGNAT
		"100.127.255.254":    true,
		"0.0.0.0":            true,
		"0.1.2.3":            true,
		"224.0.0.1":          true,
		"255.255.255.255":    true,
		"::1":                true,
		"::":                 true,
		"fe80::1":            true,
		"fc00::1":            true,
		"fd12:3456::1":       true,
		"ff02::1":            true,
		"::ffff:127.0.0.1":   true, // IPv4-mapped loopback
		"::ffff:10.0.0.1":    true,
		"64:ff9b::a9fe:a9fe": true, // NAT64 of 169.254.169.254
		"8.8.8.8":            false,
		"1.1.1.1":            false,
		"100.128.0.1":        false,
		"172.32.0.1":         false,
		"2606:4700::1111":    false,
		"64:ff9b::808:808":   false, // NAT64 of 8.8.8.8
	}
	for s, want := range cases {
		addr := netip.MustParseAddr(s)
		if got := Blocked(addr); got != want {
			t.Errorf("Blocked(%s) = %v, want %v", s, got, want)
		}
	}
}

func TestLimitedBody(t *testing.T) {
	got, err := LimitedBody(strings.NewReader("hello"), 5)
	if err != nil || string(got) != "hello" {
		t.Fatalf("exact-size body: %q, %v", got, err)
	}
	got, err = LimitedBody(strings.NewReader("hi"), 5)
	if err != nil || string(got) != "hi" {
		t.Fatalf("short body: %q, %v", got, err)
	}
	if _, err := LimitedBody(bytes.NewReader(make([]byte, 6)), 5); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("oversized body: err = %v, want ErrBodyTooLarge", err)
	}
	if _, err := LimitedBody(strings.NewReader("x"), 0); err == nil {
		t.Fatal("a non-positive limit must be rejected")
	}
}
