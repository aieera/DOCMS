//go:build integration

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/audit/internal/repository"
)

// M-14: two audit replicas appending to the same tenant's chain must never
// fork it. The old cross-instance lock was a Redis SETNX whose result was
// discarded and whose key was deleted unconditionally, so each replica only
// serialised its own goroutines: both read the same head and wrote two
// events with the same previous_hash. Each Service here has its own
// in-process mutex map, exactly like two pods.
func TestAppendChained_ConcurrentInstancesNoFork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	pool := newAuditPool(ctx, t)
	instA := New(Config{Repo: repository.New(pool), Logger: zerolog.Nop()})
	instB := New(Config{Repo: repository.New(pool), Logger: zerolog.Nop()})

	tenant := uuid.Must(uuid.NewV7()).String()
	const workersPerInstance, eventsPerWorker = 4, 10

	var wg sync.WaitGroup
	errs := make(chan error, 2*workersPerInstance*eventsPerWorker)
	for _, inst := range []*Service{instA, instB} {
		for w := 0; w < workersPerInstance; w++ {
			wg.Add(1)
			go func(inst *Service, w int) {
				defer wg.Done()
				for i := 0; i < eventsPerWorker; i++ {
					env, _ := json.Marshal(map[string]any{
						"tenantid": tenant,
						"type":     "document.viewed",
						"data": map[string]any{
							"actor_id":      uuid.Must(uuid.NewV7()).String(),
							"resource_type": "document",
							"resource_id":   uuid.Must(uuid.NewV7()).String(),
							"seq":           fmt.Sprintf("%d-%d", w, i),
						},
					})
					errs <- inst.IngestEvent(ctx, "dms.document.viewed.v1", env)
				}
			}(inst, w)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	total := 2 * workersPerInstance * eventsPerWorker
	var n, distinctPrev int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*), count(DISTINCT COALESCE(previous_hash, '')) FROM audit_events WHERE tenant_id = $1`,
		tenant).Scan(&n, &distinctPrev))
	require.Equal(t, total, n)
	require.Equal(t, total, distinctPrev,
		"every event must link to a distinct predecessor; a repeated previous_hash is a fork")

	res, err := instA.VerifyIntegrity(ctx, tenant)
	require.NoError(t, err)
	require.True(t, res.Valid, "chain must verify in (created_at, id) order: broken at %s", res.BrokenAt)
	require.EqualValues(t, total, res.Verified)
}
