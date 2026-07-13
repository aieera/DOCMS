package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/services/policy/internal/cache"
)

// PermissionCacheInvalidator subscribes to dms.permission.* events and drops
// the affected Redis decision-cache entries, so a grant / revoke / folder-ACL
// change reflects in authz IMMEDIATELY instead of waiting out the 60s TTL.
//
// Why a consumer when Grant/Revoke already invalidate locally: the shared
// Redis cache means the policy service's own writes are already fresh across
// replicas. The gap this closes is permission changes made ELSEWHERE — most
// importantly the document service's dms.permission.changed.v1 (a folder ACL
// edit), which the policy cache otherwise never hears about until the entry
// expires. Re-invalidating on our own granted/revoked events is idempotent
// and harmless.
//
// The TTL remains the safety net: a missed or failed event bounds staleness
// to cache.TTL (60s); a delivered event makes it immediate.
type PermissionCacheInvalidator struct {
	js    nats.JetStreamContext
	cache *cache.Cache
	log   zerolog.Logger
	subs  []*nats.Subscription
}

// NewPermissionCacheInvalidator wires the consumer. Call Start to subscribe.
func NewPermissionCacheInvalidator(js nats.JetStreamContext, c *cache.Cache, log zerolog.Logger) *PermissionCacheInvalidator {
	return &PermissionCacheInvalidator{js: js, cache: c, log: log.With().Str("component", "perm_cache_invalidator").Logger()}
}

// permSubject matches every permission event. NB: NATS `*` matches exactly
// one token, so `dms.permission.*` would miss the 4-token
// `dms.permission.granted.v1`; `>` matches the remaining tokens, catching
// granted/revoked/changed across versions.
const permSubject = "dms.permission.>"

// Start subscribes to dms.permission.> with a durable, manual-ack consumer.
// Returns the subscribe error so main() can log-and-continue (the service
// still serves; freshness just falls back to the TTL).
func (p *PermissionCacheInvalidator) Start(ctx context.Context) error {
	sub, err := p.js.Subscribe(permSubject, func(msg *nats.Msg) { p.handle(ctx, msg) },
		nats.Durable("policy-perm-cache-invalidator"),
		nats.ManualAck(),
		nats.MaxDeliver(5),
		nats.AckWait(30*time.Second),
	)
	if err != nil {
		return err
	}
	p.subs = append(p.subs, sub)
	p.log.Info().Str("subject", permSubject).Msg("permission cache invalidator subscribed")
	return nil
}

// Stop drains the subscriptions.
func (p *PermissionCacheInvalidator) Stop() {
	for _, s := range p.subs {
		_ = s.Drain()
	}
}

// handle invalidates the cache entries implicated by one permission event.
// Malformed events are terminated (a redelivery can't fix bad JSON); a valid
// event is acked after a best-effort invalidation — the TTL covers the rare
// Redis-unavailable case, so we don't spin on redelivery.
func (p *PermissionCacheInvalidator) handle(ctx context.Context, msg *nats.Msg) {
	data, ok := parsePermissionEvent(msg.Data)
	if !ok {
		p.log.Warn().Msg("permission event: unparseable envelope; terminating")
		_ = msg.Term()
		return
	}
	tenantID, err := uuid.Parse(strField(data, "tenant_id"))
	if err != nil {
		p.log.Warn().Str("tenant_id", strField(data, "tenant_id")).Msg("permission event: missing/invalid tenant_id; terminating")
		_ = msg.Term()
		return
	}

	invalidated := 0
	// 1. The resource whose ACL changed (folder / document / workspace).
	resourceType := strField(data, "resource_type")
	if rid, err := uuid.Parse(strField(data, "resource_id")); err == nil && resourceType != "" {
		if err := p.cache.InvalidateResource(ctx, tenantID, resourceType, rid); err == nil {
			invalidated++
		}
	}
	// 2. A group principal (granted/revoked) → every resource cached with
	//    that group must be blown away (perm_by_group fan-out).
	if strField(data, "principal_type") == "group" {
		if gid, err := uuid.Parse(strField(data, "principal_id")); err == nil {
			if n, err := p.cache.InvalidateResourcesForGroup(ctx, tenantID, gid); err == nil {
				invalidated += n
			}
		}
	}
	// 3. changed.v1 (folder ACL) carries the folder's readable groups; a
	//    group added/removed there affects that group's cached resources too.
	for _, g := range strSlice(data, "readable_by_groups") {
		if gid, err := uuid.Parse(g); err == nil {
			if n, err := p.cache.InvalidateResourcesForGroup(ctx, tenantID, gid); err == nil {
				invalidated += n
			}
		}
	}

	p.log.Debug().Str("resource_type", resourceType).Str("resource_id", strField(data, "resource_id")).
		Int("keys_invalidated", invalidated).Msg("permission cache invalidated")
	_ = msg.Ack()
}

// ---- envelope parsing -----------------------------------------------------

// parsePermissionEvent unwraps the CloudEvents envelope the outbox publisher
// produces: the domain payload is under `data`, and tenant_id rides the
// `tenantid` extension at the root (hoisted into data for uniform access).
// Mirrors the search indexer's parseData so both consumers read events the
// same way.
func parsePermissionEvent(raw []byte) (map[string]any, bool) {
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, false
	}
	data, ok := envelope["data"].(map[string]any)
	if !ok {
		data = envelope
	}
	if _, present := data["tenant_id"]; !present {
		if tid, ok := envelope["tenantid"].(string); ok && tid != "" {
			data["tenant_id"] = tid
		}
	}
	return data, true
}

func strField(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func strSlice(m map[string]any, k string) []string {
	raw, ok := m[k].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
