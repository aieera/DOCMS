package ldap

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// PoolConfig tunes the per-(tenant, ldap_config) pool.
type PoolConfig struct {
	MaxIdle     int           // max idle conns held in the pool
	MaxLifetime time.Duration // hard ceiling on a conn's age
}

// DefaultPool returns the per-ADR-0071 defaults: 4 idle, 5-min ttl.
func DefaultPool() PoolConfig { return PoolConfig{MaxIdle: 4, MaxLifetime: 5 * time.Minute} }

// pooled wraps a Client with its dial time.
type pooled struct {
	client *Client
	born   time.Time
}

// Pool holds idle LDAP connections per (tenant, config). Connections
// past MaxLifetime or that fail a WhoAmI ping are discarded on
// acquire.
//
// The pool is intentionally simple — a slice per key under a single
// mutex. Auth-service traffic for any given tenant LDAP is bursty
// (sync run + interactive logins) but never high enough to warrant
// a more elaborate scheme.
type Pool struct {
	cfg PoolConfig
	mu  sync.Mutex
	// Keyed by tenant UUID + ldap_config UUID stringified, separated
	// by a slash. Avoids a struct map key.
	idle map[string][]*pooled
}

// NewPool constructs an empty pool.
func NewPool(cfg PoolConfig) *Pool {
	if cfg.MaxIdle == 0 {
		cfg.MaxIdle = 4
	}
	if cfg.MaxLifetime == 0 {
		cfg.MaxLifetime = 5 * time.Minute
	}
	return &Pool{cfg: cfg, idle: map[string][]*pooled{}}
}

// Acquire returns a Client, dialing fresh if no usable idle conn is
// available. The caller must Release(client) when done.
func (p *Pool) Acquire(tenantID, configID uuid.UUID, dial Config) (*Client, error) {
	key := poolKey(tenantID, configID)
	p.mu.Lock()
	for len(p.idle[key]) > 0 {
		// LIFO: most-recently-released first to keep TLS sessions warm.
		n := len(p.idle[key])
		entry := p.idle[key][n-1]
		p.idle[key] = p.idle[key][:n-1]
		// Discard if too old or unhealthy.
		if time.Since(entry.born) > p.cfg.MaxLifetime || !entry.client.Healthy() {
			entry.client.Close()
			continue
		}
		p.mu.Unlock()
		return entry.client, nil
	}
	p.mu.Unlock()

	c, err := Dial(dial)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Release returns a healthy connection to the pool. A nil or unhealthy
// client is closed instead.
func (p *Pool) Release(tenantID, configID uuid.UUID, c *Client) {
	if c == nil {
		return
	}
	if !c.Healthy() {
		c.Close()
		return
	}
	key := poolKey(tenantID, configID)
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.idle[key]) >= p.cfg.MaxIdle {
		c.Close()
		return
	}
	p.idle[key] = append(p.idle[key], &pooled{client: c, born: time.Now()})
}

// CloseAll drops every pooled connection. Call on shutdown.
func (p *Pool) CloseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, list := range p.idle {
		for _, e := range list {
			e.client.Close()
		}
		delete(p.idle, k)
	}
}

func poolKey(tenantID, configID uuid.UUID) string {
	return tenantID.String() + "/" + configID.String()
}
