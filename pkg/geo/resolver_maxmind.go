//go:build maxmind

// Package geo — MaxMind GeoLite2 / GeoIP2 Commercial adapter.
//
// Gated behind the `maxmind` build tag so the default binary does
// NOT pull in the reader lib or require a `.mmdb` file. Operators
// opt in by building with `-tags maxmind` and setting one of:
//
//	VAULTDMS_GEOIP2_DB            — GeoLite2-Country.mmdb (free)
//	VAULTDMS_GEOIP2_COMMERCIAL_DB — GeoIP2-Country.mmdb   (paid)
//
// Commercial takes precedence when both are set. The resolver is
// safe for concurrent use — the underlying maxminddb-golang reader
// is designed for it.
//
// See docs/adr/0028-geoip-provider-selection.md for the rationale
// on why this is a separate adapter and docs/runbooks/15-geofencing.md
// for the refresh cadence.
package geo

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oschwald/maxminddb-golang"
)

// MaxMindResolver wraps the maxminddb reader. Reload() closes the
// previous reader and swaps in a new one atomically — used by the
// refresh cron so running requests keep using the old DB until the
// new one is fully open.
type MaxMindResolver struct {
	path   string
	reader atomic.Pointer[maxminddb.Reader]
	mu     sync.Mutex // serialises Reload; reads go lock-free via atomic.
}

// NewMaxMindResolverFromEnv reads VAULTDMS_GEOIP2_COMMERCIAL_DB or
// VAULTDMS_GEOIP2_DB (in that preference order). Returns an error
// if neither is set — the caller should fall back to StaticResolver.
func NewMaxMindResolverFromEnv() (*MaxMindResolver, error) {
	path := os.Getenv("VAULTDMS_GEOIP2_COMMERCIAL_DB")
	if path == "" {
		path = os.Getenv("VAULTDMS_GEOIP2_DB")
	}
	if path == "" {
		return nil, errors.New("geo: no MaxMind DB path set (VAULTDMS_GEOIP2_DB or VAULTDMS_GEOIP2_COMMERCIAL_DB)")
	}
	return NewMaxMindResolver(path)
}

// NewMaxMindResolver opens the .mmdb at path.
func NewMaxMindResolver(path string) (*MaxMindResolver, error) {
	r := &MaxMindResolver{path: path}
	if err := r.Reload(); err != nil {
		return nil, err
	}
	return r, nil
}

// Reload re-opens the backing .mmdb and swaps in the new reader.
// The previous reader is closed on a short delay so in-flight
// lookups can complete. Designed to be safe to call from a
// scheduled cron + from a SIGHUP handler.
func (r *MaxMindResolver) Reload() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	rd, err := maxminddb.Open(r.path)
	if err != nil {
		return fmt.Errorf("geo: open %s: %w", r.path, err)
	}
	old := r.reader.Swap(rd)
	if old != nil {
		// Leave a grace window for lookups holding the old reader.
		// 5s is generous for p99 request latency on this path.
		time.AfterFunc(5*time.Second, func() { _ = old.Close() })
	}
	return nil
}

// LookupCountry returns the ISO alpha-2 country code for ip. An
// unmatched IP returns ErrUnknown (matching the resolver interface
// contract).
func (r *MaxMindResolver) LookupCountry(ip net.IP) (string, error) {
	if ip == nil {
		return "", ErrUnknown
	}
	rd := r.reader.Load()
	if rd == nil {
		return "", errors.New("geo: MaxMind reader not loaded")
	}
	// Decode a minimal projection — only `country.iso_code` is
	// needed. Saves allocation vs. geoip2.City / geoip2.Country full
	// struct. Unknown IPs return the zero value; we map that to
	// ErrUnknown to match the interface contract.
	var record struct {
		Country struct {
			ISOCode string `maxminddb:"iso_code"`
		} `maxminddb:"country"`
	}
	if err := rd.Lookup(ip, &record); err != nil {
		return "", fmt.Errorf("geo: lookup %s: %w", ip, err)
	}
	if record.Country.ISOCode == "" {
		return "", ErrUnknown
	}
	return record.Country.ISOCode, nil
}

// Close releases the underlying reader. Tests should call this
// explicitly; long-running processes just let it live for the
// lifetime of the binary.
func (r *MaxMindResolver) Close() error {
	if rd := r.reader.Swap(nil); rd != nil {
		return rd.Close()
	}
	return nil
}

// Path returns the absolute DB path — useful for metrics +
// logs so operators can see which file the process is running
// against.
func (r *MaxMindResolver) Path() string { return r.path }
