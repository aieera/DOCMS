package regionenforcer

import (
	"strings"
	"sync"
)

// Layer enumerates the surfaces an Enforcer can validate. Each layer
// maps to a distinct config map on the Enforcer struct so a single
// region's storage endpoint can be different from its search index
// endpoint, etc.
type Layer string

const (
	LayerStorage Layer = "storage"
	LayerSearch  Layer = "search"
	LayerCache   Layer = "cache"
	LayerLog     Layer = "log"
	LayerBackup  Layer = "backup"
	LayerCDN     Layer = "cdn"
)

// Config holds one endpoint map per residency-relevant infrastructure
// layer. Keys are region codes (us-east-1, eu-west-1, …); values are
// the layer-specific identifier the runtime would route to.
//
//   - StorageEndpointByRegion: S3 / MinIO endpoint or bucket prefix
//     ("dms-eu-west-1-hot").
//   - SearchEndpointByRegion: OpenSearch cluster URL or index prefix
//     ("documents-eu-west-1-").
//   - CacheKeyspaceByRegion: Redis prefix or instance address — what
//     layer 7 reads to decide which cache to talk to.
//   - LogShipperByRegion: log-cluster ingest URL or topic.
//   - BackupBucketByRegion: cross-region backup destination bucket.
//   - CDNDistributionByRegion: CloudFront / equivalent dist id.
//
// Maps are read-only after Enforcer construction. Adding regions at
// runtime is intentionally not supported — region rollout is a
// deploy-time concern that should land via config + restart, not a
// hot-reload path that could half-update the enforcer.
type Config struct {
	StorageEndpointByRegion  map[string]string
	SearchEndpointByRegion   map[string]string
	CacheKeyspaceByRegion    map[string]string
	LogShipperByRegion       map[string]string
	BackupBucketByRegion     map[string]string
	CDNDistributionByRegion  map[string]string
}

// Enforcer validates that an outbound request's target endpoint
// matches the source document's region_pin. Failed validations are
// the canonical REGION_VIOLATION trigger — the caller wraps the
// returned error with its own audit emit + 451 response.
type Enforcer struct {
	cfg Config
	// Pre-built reverse index: endpoint string → region code. Lets
	// Validate run in O(1) without scanning every map on every call.
	mu      sync.RWMutex
	reverse map[Layer]map[string]string
}

// New returns an Enforcer pre-indexed for fast lookups. Empty maps in
// the config are tolerated — Validate against an unconfigured layer
// returns layer_mismatch rather than panicking, which is what we want
// when a tenant is on a service tier that doesn't include e.g. CDN.
func New(cfg Config) *Enforcer {
	e := &Enforcer{cfg: cfg, reverse: make(map[Layer]map[string]string, 6)}
	for layer, m := range map[Layer]map[string]string{
		LayerStorage: cfg.StorageEndpointByRegion,
		LayerSearch:  cfg.SearchEndpointByRegion,
		LayerCache:   cfg.CacheKeyspaceByRegion,
		LayerLog:     cfg.LogShipperByRegion,
		LayerBackup:  cfg.BackupBucketByRegion,
		LayerCDN:     cfg.CDNDistributionByRegion,
	} {
		idx := make(map[string]string, len(m))
		for region, ep := range m {
			idx[strings.ToLower(strings.TrimSpace(ep))] = region
		}
		e.reverse[layer] = idx
	}
	return e
}

// Validate reports whether `endpoint` is the configured target for
// the given (region, layer) tuple. Returns *ErrRegionViolation on
// mismatch with a Reason that maps cleanly to the runbook triage
// table.
func (e *Enforcer) Validate(region string, layer Layer, endpoint string) error {
	if !IsKnown(region) {
		return &ErrRegionViolation{DocumentRegion: region, TargetRegion: "", Layer: string(layer), Reason: "unknown_region"}
	}
	e.mu.RLock()
	idx, ok := e.reverse[layer]
	e.mu.RUnlock()
	if !ok || len(idx) == 0 {
		// Layer isn't configured for this deployment — refuse rather
		// than silently passing. This is the failure mode if a new
		// service starts hitting a layer the enforcer wasn't told
		// about; better to 451 than to leak data.
		return &ErrRegionViolation{DocumentRegion: region, TargetRegion: "", Layer: string(layer), Reason: "layer_mismatch"}
	}
	target, ok := idx[strings.ToLower(strings.TrimSpace(endpoint))]
	if !ok {
		return &ErrRegionViolation{DocumentRegion: region, TargetRegion: endpoint, Layer: string(layer), Reason: "unknown_region"}
	}
	if !strings.EqualFold(target, region) {
		if !SameBoundary(region, target) {
			return &ErrRegionViolation{DocumentRegion: region, TargetRegion: target, Layer: string(layer), Reason: "cross_boundary"}
		}
		return &ErrRegionViolation{DocumentRegion: region, TargetRegion: target, Layer: string(layer), Reason: "layer_mismatch"}
	}
	return nil
}

// EndpointFor returns the configured endpoint for (region, layer), or
// "" if unconfigured. Callers use this to route writes — picking the
// endpoint themselves and then optionally cross-checking with
// Validate is the canonical pattern.
func (e *Enforcer) EndpointFor(region string, layer Layer) string {
	switch layer {
	case LayerStorage:
		return e.cfg.StorageEndpointByRegion[region]
	case LayerSearch:
		return e.cfg.SearchEndpointByRegion[region]
	case LayerCache:
		return e.cfg.CacheKeyspaceByRegion[region]
	case LayerLog:
		return e.cfg.LogShipperByRegion[region]
	case LayerBackup:
		return e.cfg.BackupBucketByRegion[region]
	case LayerCDN:
		return e.cfg.CDNDistributionByRegion[region]
	}
	return ""
}
