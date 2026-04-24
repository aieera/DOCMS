// Package geo resolves a source IP to an ISO 3166-1 alpha-2 country
// code. Two implementations ship:
//
//   - StaticResolver: an operator-provided CIDR→country map fed by the
//     VAULTDMS_GEO_STATIC env var (semicolon-separated
//     "<cidr>=<cc>" pairs). Used in dev, air-gapped deploys, and as
//     a deterministic fallback.
//   - CachingResolver: wraps any Resolver with a short in-memory TTL
//     cache so repeated lookups for the same /24 don't pay the lookup
//     cost on the hot request path.
//
// MaxMind GeoLite2 / GeoIP2 Commercial is the production target; see
// docs/adr/0028-geoip-provider-selection.md. The adapter is gated
// behind a build tag to keep the library out of the binary when not
// configured, and lives in resolver_maxmind.go (tracked as Wave 15.2
// follow-up — the MaxMind Go reader is a new dep that requires ADR
// sign-off before landing).
package geo

import (
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// Resolver maps an IP to a 2-letter country code. Implementations
// MUST be safe for concurrent use. An empty string result is the
// canonical "unknown" value — callers decide how to handle it (the
// geofence service treats unknown as neither in allow nor in deny
// sets, which means `step_up` policies WILL trigger but `allow`
// policies will NOT satisfy).
type Resolver interface {
	LookupCountry(ip net.IP) (string, error)
}

// ErrUnknown is what a Resolver returns when it has no opinion.
// Callers should NOT treat this as an error state — they should
// feed it through the decision layer, which applies the per-tenant
// "unknown country" rule (today: falls through to step_up policies).
var ErrUnknown = errors.New("geo: country unknown")

// StaticResolver is a fixed CIDR→country map. Loaded from the
// `VAULTDMS_GEO_STATIC` env var at construction time; missing or
// empty var yields a resolver that returns ErrUnknown for every
// lookup. Entries are matched in insertion order and the first
// matching CIDR wins.
//
// Example env: "203.0.113.0/24=US;198.51.100.0/24=DE"
type StaticResolver struct {
	entries []staticEntry
}

type staticEntry struct {
	net     *net.IPNet
	country string
}

// NewStaticResolverFromEnv reads VAULTDMS_GEO_STATIC. Returns an
// empty resolver if the env var is unset — safe to use; every
// lookup returns ErrUnknown.
func NewStaticResolverFromEnv() (*StaticResolver, error) {
	return NewStaticResolver(os.Getenv("VAULTDMS_GEO_STATIC"))
}

// NewStaticResolver parses a semicolon-separated "<cidr>=<cc>" list.
func NewStaticResolver(raw string) (*StaticResolver, error) {
	r := &StaticResolver{}
	if strings.TrimSpace(raw) == "" {
		return r, nil
	}
	for _, pair := range strings.Split(raw, ";") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			return nil, &parseError{entry: pair, reason: "missing '='"}
		}
		_, ipn, err := net.ParseCIDR(strings.TrimSpace(kv[0]))
		if err != nil {
			return nil, &parseError{entry: pair, reason: err.Error()}
		}
		cc := strings.ToUpper(strings.TrimSpace(kv[1]))
		if len(cc) != 2 {
			return nil, &parseError{entry: pair, reason: "country code must be 2 letters"}
		}
		r.entries = append(r.entries, staticEntry{net: ipn, country: cc})
	}
	return r, nil
}

// LookupCountry returns the country for the first matching CIDR or
// ErrUnknown when no entry matches.
func (r *StaticResolver) LookupCountry(ip net.IP) (string, error) {
	if ip == nil {
		return "", ErrUnknown
	}
	for _, e := range r.entries {
		if e.net.Contains(ip) {
			return e.country, nil
		}
	}
	return "", ErrUnknown
}

type parseError struct {
	entry  string
	reason string
}

func (e *parseError) Error() string {
	return "geo static parse: " + e.entry + ": " + e.reason
}

// CachingResolver wraps a Resolver with a per-IP TTL cache. Keyed by
// the full IP; callers that want /24-granularity caching should mask
// the IP before invoking LookupCountry. The cache is bounded — a
// simple eviction when `maxEntries` is hit prevents unbounded memory
// growth under IP-enumeration traffic. The policy is "wipe half on
// overflow" rather than LRU; acceptable because TTL is already short
// (seconds) and correctness doesn't depend on cache state.
type CachingResolver struct {
	inner      Resolver
	ttl        time.Duration
	maxEntries int

	mu    sync.RWMutex
	cache map[string]cacheEntry
}

// DefaultCacheMaxEntries caps the cache. At 60 s TTL and ~100 B per
// entry that's ~1 MB — safe for every service.
const DefaultCacheMaxEntries = 10_000

type cacheEntry struct {
	country string
	expires time.Time
	// err is preserved so ErrUnknown is cached alongside hits and
	// doesn't thrash the underlying resolver on repeated misses.
	err error
}

// NewCachingResolver wraps inner with a TTL cache. ttl must be > 0.
// Default in callers: 60 s per the Wave 15.2 brief.
func NewCachingResolver(inner Resolver, ttl time.Duration) *CachingResolver {
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	return &CachingResolver{
		inner:      inner,
		ttl:        ttl,
		maxEntries: DefaultCacheMaxEntries,
		cache:      map[string]cacheEntry{},
	}
}

// LookupCountry checks the cache first, then falls through to the
// underlying resolver. Cache misses and ErrUnknown results are both
// cached for the TTL so pathological traffic doesn't hammer the
// backend.
func (c *CachingResolver) LookupCountry(ip net.IP) (string, error) {
	if ip == nil {
		return "", ErrUnknown
	}
	key := ip.String()
	c.mu.RLock()
	ent, ok := c.cache[key]
	c.mu.RUnlock()
	if ok && time.Now().Before(ent.expires) {
		return ent.country, ent.err
	}
	cc, err := c.inner.LookupCountry(ip)
	c.mu.Lock()
	// Over-capacity? Drop every second entry — cheap, no ordering
	// bookkeeping, and the 60 s TTL makes this a rare event in
	// practice. Under 1 MB of map state, so even a pathological
	// eviction is fast.
	if c.maxEntries > 0 && len(c.cache) >= c.maxEntries {
		i := 0
		for k := range c.cache {
			if i%2 == 0 {
				delete(c.cache, k)
			}
			i++
		}
	}
	c.cache[key] = cacheEntry{country: cc, expires: time.Now().Add(c.ttl), err: err}
	c.mu.Unlock()
	return cc, err
}
