package netguard

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
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
	resp, err := ctxGet(c, srv.URL)
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

	resp, err := ctxGet(NewClient(Options{AllowPrivate: true}), srv.URL)
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

	resp, err := ctxGet(NewClient(Options{AllowPrivate: true}), srv.URL)
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
		"::7f00:1":           true, // IPv4-compatible 127.0.0.1
		"::a00:1":            true, // IPv4-compatible 10.0.0.1
		"::808:808":          true, // IPv4-compatible (deprecated) is refused outright
		"192.88.99.1":        true, // 6to4 relay anycast
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

// A redirect is dialed through the same guard: the first hop is allowed
// (exempted here, standing in for a public host), the loopback target of
// its redirect is not.
func TestNewClient_RedirectToLoopbackRefused(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
	}))
	defer target.Close()
	var firstHits atomic.Int32
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits.Add(1)
		http.Redirect(w, r, target.URL+"/internal", http.StatusFound)
	}))
	defer redirector.Close()

	first := netip.MustParseAddrPort(redirector.Listener.Addr().String())
	setTestHooks(t, func(ap netip.AddrPort) bool { return ap == first }, nil)

	resp, err := ctxGet(NewClient(Options{}), redirector.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("the redirect to a loopback target must be refused")
	}
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v, want ErrBlockedAddress", err)
	}
	if firstHits.Load() != 1 {
		t.Fatalf("the (exempted) first hop was reached %d times, want 1", firstHits.Load())
	}
	if n := targetHits.Load(); n != 0 {
		t.Fatalf("the loopback target was reached %d times", n)
	}
}

// localhost resolves through the system resolver to a loopback address.
func TestNewClient_LocalhostNameRefused(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer srv.Close()
	port := netip.MustParseAddrPort(srv.Listener.Addr().String()).Port()
	resp, err := ctxGet(NewClient(Options{}), fmt.Sprintf("http://localhost:%d/", port))
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("localhost must be refused")
	}
	if !errors.Is(err, ErrBlockedAddress) || hits.Load() != 0 {
		t.Fatalf("err = %v, hits = %d", err, hits.Load())
	}
}

// A public-looking hostname whose DNS answer is an RFC 1918 address (DNS
// rebinding) is refused on the resolved address, before any connection.
func TestNewClient_HostnameResolvingToPrivateRefused(t *testing.T) {
	pc, err := (&net.ListenConfig{}).ListenPacket(context.Background(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go serveDNS(pc, [4]byte{10, 0, 0, 1})
	setTestHooks(t, nil, &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "udp", pc.LocalAddr().String())
	}})

	start := time.Now()
	resp, err := ctxGet(NewClient(Options{Timeout: 5 * time.Second}), "http://intranet.example.com/")
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("a name resolving to 10.0.0.1 must be refused")
	}
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v, want ErrBlockedAddress", err)
	}
	if !strings.Contains(err.Error(), "10.0.0.1") {
		t.Fatalf("err = %v, want the resolved 10.0.0.1 named", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("refusal must happen at dial time, not after a connect timeout")
	}
}

// serveDNS answers every A query with addr and every other query with no
// records (enough for the Go resolver).
func serveDNS(pc net.PacketConn, addr [4]byte) {
	buf := make([]byte, 1500)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		q := buf[:n]
		if len(q) < 12 {
			continue
		}
		i := 12
		for i < len(q) && q[i] != 0 {
			i += int(q[i]) + 1
		}
		if i+5 > len(q) {
			continue
		}
		qtype := binary.BigEndian.Uint16(q[i+1:])
		question := q[12 : i+5]
		resp := make([]byte, 12, 64)
		copy(resp, q[:2])
		binary.BigEndian.PutUint16(resp[2:], 0x8180)
		binary.BigEndian.PutUint16(resp[4:], 1)
		if qtype == 1 {
			binary.BigEndian.PutUint16(resp[6:], 1)
		}
		resp = append(resp, question...)
		if qtype == 1 {
			resp = append(resp, 0xC0, 0x0C, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4)
			resp = append(resp, addr[:]...)
		}
		_, _ = pc.WriteTo(resp, from)
	}
}

// setTestHooks installs the package test hooks for one test and restores
// them with t.Cleanup. Tests that call it must not call t.Parallel: the
// hooks are package state, read by NewClient.
func setTestHooks(t *testing.T, exempt func(netip.AddrPort) bool, resolver *net.Resolver) {
	t.Helper()
	if dialExempt != nil || testResolver != nil {
		t.Fatal("netguard test hooks already set: a hook test is running in parallel or leaked")
	}
	dialExempt, testResolver = exempt, resolver
	t.Cleanup(func() { dialExempt, testResolver = nil, nil })
}

// ctxGet is (*http.Client).Get with an explicit context (noctx).
func ctxGet(c *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}
