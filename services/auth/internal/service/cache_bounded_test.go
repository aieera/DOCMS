package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/vaultdms/vaultdms/services/auth/internal/model"
)

// Blueprint §8.1 checklist item: "Redis memory growth under session load
// is bounded". A 10k-login load test against real Redis is the prod
// signal; this unit-level test uses miniredis + FastForward to pin the
// invariant that every cache entry has a TTL ≤ session expiry AND that
// expiry causes complete drain. If that property breaks, prod Redis
// memory grows unbounded no matter how fast login traffic is.

func TestCacheSession_10KEntries_AllDrainedAfterTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	svc := &Service{
		rdb: rdb,
		now: time.Now,
	}

	const (
		loadSize = 10_000
		ttl      = time.Minute // short enough that FastForward completes fast; real TTLs are hours
	)
	tenantID := uuid.New()
	expiresAt := time.Now().Add(ttl)

	// Pump 10k cache writes. Every call writes TWO keys (indirection +
	// entry) per the session.go sessionKey / sessionTenantKey scheme —
	// so we expect 20k keys in miniredis.
	for i := 0; i < loadSize; i++ {
		hash := fmt.Sprintf("hash-%08x", i)
		svc.cacheSession(context.Background(), hash, &model.CachedSession{
			UserID:    uuid.New(),
			TenantID:  tenantID,
			Email:     "load@example.com",
			Role:      model.Role("user"),
			ExpiresAt: expiresAt,
		})
	}

	require.Len(t, mr.Keys(), loadSize*2,
		"each session writes two keys: session:{tenant}:{hash} + session_tenant:{hash}")

	// Sanity-check TTL is bounded (not PERSIST'd / never-expires). A
	// single zero-TTL key would be a silent memory leak under load.
	for _, k := range mr.Keys() {
		require.Positive(t, int64(mr.TTL(k)), "key %q missing TTL; would leak under sustained login load", k)
		require.LessOrEqual(t, mr.TTL(k), ttl, "key %q TTL exceeds session expiry", k)
	}

	// Advance past expiry — miniredis collapses expired keys lazily on
	// access, so Keys() alone doesn't reflect expiry. FastForward walks
	// the keyspace and evicts anything past its TTL.
	mr.FastForward(ttl + time.Second)

	require.Empty(t, mr.Keys(), "all cache keys must be reaped after TTL — otherwise Redis grows unbounded under login load")
}

// TestCacheSession_ZeroOrNegativeTTL_NoWrite pins the safety hatch that
// stops the loop above from writing a never-expiring entry when a
// caller accidentally hands in an already-expired CachedSession.
// Without this check, replaying an old token would plant a stale key
// with TTL 0 (no expiry) — exactly the leak this test guards against.
func TestCacheSession_ExpiredSession_SkipsWrite(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	svc := &Service{rdb: rdb, now: time.Now}

	svc.cacheSession(context.Background(), "stale-hash", &model.CachedSession{
		UserID:    uuid.New(),
		TenantID:  uuid.New(),
		ExpiresAt: time.Now().Add(-1 * time.Minute), // already expired
	})

	require.Empty(t, mr.Keys(), "already-expired sessions must not be cached")
}
