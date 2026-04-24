package trustedproxy

import (
	"net"
	"net/http"
	"net/netip"
	"sync/atomic"
)

// defaultCfg is the process-wide default. Services call SetDefault
// once at boot after LoadFromEnv; middleware constructors that don't
// want to thread a Config can reach for RealClientIPDefault /
// RealClientIPString instead.
var defaultCfg atomic.Pointer[Config]

// SetDefault installs the process-wide Config. Safe to call
// concurrently; last writer wins. Call once at service startup.
func SetDefault(cfg Config) {
	defaultCfg.Store(&cfg)
}

// Default returns the installed default Config. Returns an empty
// Config (no trusted CIDRs) if SetDefault was never called — in that
// case RealClientIPDefault degrades to r.RemoteAddr, which is always
// safe.
func Default() Config {
	if p := defaultCfg.Load(); p != nil {
		return *p
	}
	return Config{}
}

// RealClientIPDefault is RealClientIP using the process-wide default
// Config. Prefer this in middleware unless you have a reason to hold
// your own Config.
func RealClientIPDefault(r *http.Request) netip.Addr {
	return RealClientIP(r, Default())
}

// RealClientIPNetIP is an adapter that returns net.IP for callers
// still using the older type (e.g., geofence middleware that threads
// the IP through a decider API). Zero netip.Addr maps to nil net.IP.
func RealClientIPNetIP(r *http.Request, cfg Config) net.IP {
	addr := RealClientIP(r, cfg)
	if !addr.IsValid() {
		return nil
	}
	// netip.Addr.AsSlice() returns a 4-byte slice for v4 and
	// 16-byte for v6 — net.IP accepts either.
	b := addr.AsSlice()
	return net.IP(b)
}

// RealClientIPString is a convenience for callers that log or hash a
// string form (rate limiter keys, audit records). Returns the empty
// string when nothing is resolvable.
func RealClientIPString(r *http.Request) string {
	addr := RealClientIPDefault(r)
	if !addr.IsValid() {
		return ""
	}
	return addr.String()
}
