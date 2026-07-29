// Package cache is the Redis-backed permission/group cache that keeps
// CheckPermission p99 under 5ms. TTL is 60s; explicit invalidation on
// grant/revoke keeps data fresh in the common case.
package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// TTL is the default cache TTL. Exposed so tests can override.
const TTL = 60 * time.Second

// Cache wraps Redis. Zero value is not usable — use New.
type Cache struct {
	rdb *redis.Client
}

// New wires a Cache.
func New(rdb *redis.Client) *Cache { return &Cache{rdb: rdb} }

// ---- keys -----------------------------------------------------------------

// ResourcePermsKey holds the permissions for a single resource.
// Format: perm:{tenant}:{resource_type}:{resource_id}
func ResourcePermsKey(tenantID uuid.UUID, resourceType string, resourceID uuid.UUID) string {
	return fmt.Sprintf("perm:%s:%s:%s", tenantID, resourceType, resourceID)
}

// UserGroupsKey caches the group-membership of a user.
// Format: groups:{tenant}:{user}
func UserGroupsKey(tenantID, userID uuid.UUID) string {
	return fmt.Sprintf("groups:%s:%s", tenantID, userID)
}

// UserWorkspacesKey caches the workspace_members rows for a user.
// Format: workspaces:{tenant}:{user}
func UserWorkspacesKey(tenantID, userID uuid.UUID) string {
	return fmt.Sprintf("workspaces:%s:%s", tenantID, userID)
}

// ResourcePermsSetKey is a Redis set of resource IDs that have a permission
// entry involving a given group. Used on group-permission revoke to find
// which resources' caches to blow away.
// Format: perm_by_group:{tenant}:{group}
func ResourcePermsSetKey(tenantID, groupID uuid.UUID) string {
	return fmt.Sprintf("perm_by_group:%s:%s", tenantID, groupID)
}

// ---- generic helpers ------------------------------------------------------

// GetJSON reads and unmarshals a key. Returns (false, nil) on cache miss
// so callers can distinguish miss from error cleanly.
func (c *Cache) GetJSON(ctx context.Context, key string, out any) (hit bool, err error) {
	b, err := c.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, out); err != nil {
		// Corrupted entry — treat as miss + delete.
		_ = c.rdb.Del(ctx, key).Err()
		return false, nil
	}
	return true, nil
}

// SetJSON marshals + writes with default TTL.
func (c *Cache) SetJSON(ctx context.Context, key string, v any) error {
	return c.SetJSONTTL(ctx, key, v, TTL)
}

// MembershipTTL bounds how long a stale user->groups / user->workspaces set can
// keep granting access after a membership or workspace-role change. It is kept
// short because those mutations happen in ANOTHER service and emit no event this
// cache can consume (Epic 7 #2/#3), so TTL expiry is currently the ONLY
// invalidation for these keys. See docs/security/epic7-policy-followups.md.
const MembershipTTL = 5 * time.Second

// SetJSONTTL marshals + writes with an explicit TTL.
func (c *Cache) SetJSONTTL(ctx context.Context, key string, v any, ttl time.Duration) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, key, b, ttl).Err()
}

// Del removes a single key. Missing keys are not an error.
func (c *Cache) Del(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return c.rdb.Del(ctx, keys...).Err()
}

// ---- invalidation helpers -------------------------------------------------

// InvalidateResource drops a resource's cached permission list.
func (c *Cache) InvalidateResource(ctx context.Context, tenantID uuid.UUID, kind string, resourceID uuid.UUID) error {
	return c.Del(ctx, ResourcePermsKey(tenantID, kind, resourceID))
}

// InvalidateUserGroups drops a user's cached group list. Should be called on
// group_members mutations — but see Epic 7 #2: no group-membership event is
// currently emitted for this to subscribe to, so MembershipTTL bounds staleness.
func (c *Cache) InvalidateUserGroups(ctx context.Context, tenantID, userID uuid.UUID) error {
	return c.Del(ctx, UserGroupsKey(tenantID, userID))
}

// InvalidateUserWorkspaces drops a user's cached workspace-membership+role list.
// Should be called on workspace_members mutations (add/remove/role change) — but
// see Epic 7 #3: no workspace-membership event is currently emitted, so
// MembershipTTL bounds staleness until one is.
func (c *Cache) InvalidateUserWorkspaces(ctx context.Context, tenantID, userID uuid.UUID) error {
	return c.Del(ctx, UserWorkspacesKey(tenantID, userID))
}

// TrackResourceByGroup records that resource X has a permission involving
// group Y. When Y's membership changes we can blow away only the right
// resource caches via ResourcePermsSetKey.
func (c *Cache) TrackResourceByGroup(ctx context.Context, tenantID, groupID uuid.UUID, resourceKind string, resourceID uuid.UUID) error {
	member := resourceKind + ":" + resourceID.String()
	return c.rdb.SAdd(ctx, ResourcePermsSetKey(tenantID, groupID), member).Err()
}

// InvalidateResourcesForGroup drops every resource permission cache that
// the given group has a permission on. Returns how many keys were blown.
func (c *Cache) InvalidateResourcesForGroup(ctx context.Context, tenantID, groupID uuid.UUID) (int, error) {
	members, err := c.rdb.SMembers(ctx, ResourcePermsSetKey(tenantID, groupID)).Result()
	if err != nil {
		return 0, err
	}
	var keys []string
	for _, m := range members {
		keys = append(keys, "perm:"+tenantID.String()+":"+m)
	}
	if len(keys) == 0 {
		return 0, nil
	}
	return len(keys), c.Del(ctx, keys...)
}
