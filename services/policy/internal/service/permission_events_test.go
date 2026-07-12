package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/policy/internal/cache"
)

// newTestCache returns a Cache backed by an in-memory miniredis (real Redis
// protocol, no container) plus the raw client for TTL inspection.
func newTestCache(t *testing.T) (*cache.Cache, *redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return cache.New(rdb), rdb, mr
}

// permEnvelope builds the CloudEvents envelope the outbox publisher emits:
// tenant on the `tenantid` extension, payload under `data`.
func permEnvelope(eventType, tenantID string, data map[string]any) *nats.Msg {
	env := map[string]any{
		"specversion": "1.0",
		"type":        eventType,
		"tenantid":    tenantID,
		"data":        data,
	}
	b, _ := json.Marshal(env)
	return &nats.Msg{Subject: eventType, Data: b}
}

func newInvalidator(c *cache.Cache) *PermissionCacheInvalidator {
	return &PermissionCacheInvalidator{cache: c, log: zerolog.Nop()}
}

// A revoke event must drop the resource's cached permissions immediately, so
// the next Check re-reads from Postgres and reflects the revoke — rather than
// serving the stale "allowed" entry until the 60s TTL.
func TestPermissionInvalidator_RevokeInvalidatesResourceImmediately(t *testing.T) {
	c, _, _ := newTestCache(t)
	ctx := context.Background()
	tenant := uuid.Must(uuid.NewV7())
	resID := uuid.Must(uuid.NewV7())

	// Seed the decision cache with an "allowed" permission list.
	key := cache.ResourcePermsKey(tenant, "document", resID)
	require.NoError(t, c.SetJSON(ctx, key, []map[string]string{{"capability": "view"}}))
	hit, err := c.GetJSON(ctx, key, &[]map[string]string{})
	require.NoError(t, err)
	require.True(t, hit, "precondition: entry is cached")

	// Revoke event for that resource.
	newInvalidator(c).handle(ctx, permEnvelope("dms.permission.revoked.v1", tenant.String(), map[string]any{
		"resource_type":  "document",
		"resource_id":    resID.String(),
		"principal_type": "user",
		"principal_id":   uuid.NewString(),
		"capability":     "view",
	}))

	hit, err = c.GetJSON(ctx, key, &[]map[string]string{})
	require.NoError(t, err)
	require.False(t, hit, "revoke event must invalidate the resource cache immediately")
}

// A group grant/revoke must fan out to every resource cached with that group
// (perm_by_group), not just one resource id.
func TestPermissionInvalidator_GroupEventFansOutToResources(t *testing.T) {
	c, _, _ := newTestCache(t)
	ctx := context.Background()
	tenant := uuid.Must(uuid.NewV7())
	groupID := uuid.Must(uuid.NewV7())
	resID := uuid.Must(uuid.NewV7())

	// A resource cached, tracked as involving the group.
	key := cache.ResourcePermsKey(tenant, "document", resID)
	require.NoError(t, c.SetJSON(ctx, key, []map[string]string{{"capability": "edit"}}))
	require.NoError(t, c.TrackResourceByGroup(ctx, tenant, groupID, "document", resID))

	// A grant to the GROUP on a DIFFERENT resource — the group's cached
	// resources must still be blown away.
	newInvalidator(c).handle(ctx, permEnvelope("dms.permission.granted.v1", tenant.String(), map[string]any{
		"resource_type":  "folder",
		"resource_id":    uuid.NewString(),
		"principal_type": "group",
		"principal_id":   groupID.String(),
		"capability":     "view",
	}))

	hit, err := c.GetJSON(ctx, key, &[]map[string]string{})
	require.NoError(t, err)
	require.False(t, hit, "a group permission change must invalidate the group's cached resources")
}

// The document service's dms.permission.changed.v1 (a folder ACL edit) must
// invalidate the folder's cache AND every group named in readable_by_groups
// — this is the cross-service case the policy cache otherwise never heard.
func TestPermissionInvalidator_FolderACLChangeInvalidatesFolderAndGroups(t *testing.T) {
	c, _, _ := newTestCache(t)
	ctx := context.Background()
	tenant := uuid.Must(uuid.NewV7())
	folderID := uuid.Must(uuid.NewV7())
	groupID := uuid.Must(uuid.NewV7())
	docID := uuid.Must(uuid.NewV7())

	folderKey := cache.ResourcePermsKey(tenant, "folder", folderID)
	docKey := cache.ResourcePermsKey(tenant, "document", docID)
	require.NoError(t, c.SetJSON(ctx, folderKey, []map[string]string{{"capability": "view"}}))
	require.NoError(t, c.SetJSON(ctx, docKey, []map[string]string{{"capability": "view"}}))
	require.NoError(t, c.TrackResourceByGroup(ctx, tenant, groupID, "document", docID))

	newInvalidator(c).handle(ctx, permEnvelope("dms.permission.changed.v1", tenant.String(), map[string]any{
		"tenant_id":          tenant.String(),
		"resource_type":      "folder",
		"resource_id":        folderID.String(),
		"workspace_id":       uuid.NewString(),
		"readable_by_groups": []any{groupID.String()},
	}))

	for name, key := range map[string]string{"folder": folderKey, "doc-via-group": docKey} {
		hit, err := c.GetJSON(ctx, key, &[]map[string]string{})
		require.NoError(t, err)
		require.Falsef(t, hit, "folder ACL change must invalidate the %s cache", name)
	}
}

// The TTL is the safety net: even without an event, a cached decision cannot
// go stale for longer than cache.TTL. Pins the "within the cache-TTL bound"
// half of the guarantee.
func TestPermissionInvalidator_TTLBoundsStaleness(t *testing.T) {
	c, _, mr := newTestCache(t)
	ctx := context.Background()
	key := cache.ResourcePermsKey(uuid.Must(uuid.NewV7()), "document", uuid.Must(uuid.NewV7()))
	require.NoError(t, c.SetJSON(ctx, key, []string{"x"}))

	ttl := mr.TTL(key)
	require.Greater(t, ttl, time.Duration(0), "cached decisions must carry a TTL (bounded staleness)")
	require.LessOrEqual(t, ttl, cache.TTL, "TTL must not exceed the documented bound")
}

// A malformed / non-permission event must not panic or invalidate anything.
func TestPermissionInvalidator_MalformedEventIsSafe(t *testing.T) {
	c, _, _ := newTestCache(t)
	ctx := context.Background()
	require.NotPanics(t, func() {
		newInvalidator(c).handle(ctx, &nats.Msg{Subject: "dms.permission.granted.v1", Data: []byte("{not json")})
		// valid envelope, missing tenant → terminated, no crash.
		newInvalidator(c).handle(ctx, permEnvelope("dms.permission.granted.v1", "", map[string]any{"resource_id": "x"}))
	})
}
