package trustedproxy

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func mustConfig(t *testing.T, cidrs string) Config {
	t.Helper()
	cfg, err := Parse(cidrs)
	if err != nil {
		t.Fatalf("Parse(%q): %v", cidrs, err)
	}
	return cfg
}

func TestParse_Valid(t *testing.T) {
	cfg := mustConfig(t, "10.0.0.0/8, 192.168.0.0/16 ")
	if len(cfg.TrustedCIDRs) != 2 {
		t.Fatalf("want 2 prefixes, got %d", len(cfg.TrustedCIDRs))
	}
}

func TestParse_Empty(t *testing.T) {
	cfg := mustConfig(t, "")
	if len(cfg.TrustedCIDRs) != 0 {
		t.Fatalf("want 0 prefixes, got %d", len(cfg.TrustedCIDRs))
	}
}

func TestParse_Invalid(t *testing.T) {
	if _, err := Parse("not-a-cidr"); err == nil {
		t.Fatal("want error, got nil")
	}
}

func TestLoadFromEnv_ProductionEmptyPanics(t *testing.T) {
	t.Setenv(EnvTrustedCIDRs, "")
	t.Setenv(EnvAppEnv, "production")
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("want panic on empty CIDR list in production")
		}
	}()
	LoadFromEnv()
}

func TestLoadFromEnv_DevEmptyOK(t *testing.T) {
	t.Setenv(EnvTrustedCIDRs, "")
	t.Setenv(EnvAppEnv, "development")
	cfg := LoadFromEnv()
	if len(cfg.TrustedCIDRs) != 0 {
		t.Fatalf("want empty config in dev, got %d", len(cfg.TrustedCIDRs))
	}
}

func makeRequest(remote, xff string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = remote
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	return r
}

func addr(s string) netip.Addr {
	a, _ := netip.ParseAddr(s)
	return a
}

func TestRealClientIP_EmptyXFF_FallsBackToRemoteAddr(t *testing.T) {
	cfg := mustConfig(t, "10.0.0.0/8")
	r := makeRequest("8.8.8.8:1234", "")
	got := RealClientIP(r, cfg)
	if got != addr("8.8.8.8") {
		t.Fatalf("want 8.8.8.8, got %v", got)
	}
}

func TestRealClientIP_UntrustedPeer_IgnoresXFF(t *testing.T) {
	// Peer itself is not a trusted proxy → XFF is attacker-set, ignore.
	cfg := mustConfig(t, "10.0.0.0/8")
	r := makeRequest("8.8.8.8:1234", "1.1.1.1, 2.2.2.2")
	got := RealClientIP(r, cfg)
	if got != addr("8.8.8.8") {
		t.Fatalf("want 8.8.8.8 (untrusted peer), got %v", got)
	}
}

func TestRealClientIP_AllTrustedChain_ReturnsLeftmost(t *testing.T) {
	// Peer and every XFF hop in trust zone: return the left-most XFF
	// entry as the best-guess client.
	cfg := mustConfig(t, "10.0.0.0/8")
	r := makeRequest("10.0.0.5:80", "203.0.113.7, 10.0.0.3, 10.0.0.4")
	got := RealClientIP(r, cfg)
	if got != addr("203.0.113.7") {
		t.Fatalf("want 203.0.113.7, got %v", got)
	}
}

func TestRealClientIP_UntrustedIntermediate_ReturnsThatHop(t *testing.T) {
	// Peer trusted; rightmost XFF is trusted; the next hop left is
	// external — that's the real client.
	cfg := mustConfig(t, "10.0.0.0/8")
	r := makeRequest("10.0.0.5:80", "198.51.100.9, 10.0.0.4")
	got := RealClientIP(r, cfg)
	if got != addr("198.51.100.9") {
		t.Fatalf("want 198.51.100.9, got %v", got)
	}
}

func TestRealClientIP_IPv4MappedIPv6Normalised(t *testing.T) {
	// Peer arrives as ::ffff:10.0.0.5 but the CIDR is in v4 form.
	cfg := mustConfig(t, "10.0.0.0/8")
	r := makeRequest("[::ffff:10.0.0.5]:80", "203.0.113.7")
	got := RealClientIP(r, cfg)
	if got != addr("203.0.113.7") {
		t.Fatalf("want 203.0.113.7, got %v", got)
	}
}

func TestRealClientIP_UnparseableHop_BreaksChain(t *testing.T) {
	// An invalid entry means we can't trust what came before it —
	// fall back to the last-known-good hop on its right.
	cfg := mustConfig(t, "10.0.0.0/8")
	r := makeRequest("10.0.0.5:80", "bogus, 10.0.0.4")
	got := RealClientIP(r, cfg)
	// The hop on the right of "bogus" is 10.0.0.4 (trusted); that's
	// the closest legitimate source we can name.
	if got != addr("10.0.0.4") {
		t.Fatalf("want 10.0.0.4 (last-known-good), got %v", got)
	}
}

func TestTrusts_InvalidAddr(t *testing.T) {
	cfg := mustConfig(t, "10.0.0.0/8")
	if cfg.Trusts(netip.Addr{}) {
		t.Fatal("zero addr must never be trusted")
	}
}
