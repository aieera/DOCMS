//go:build integration

package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/audit/internal/model"
	"github.com/aieera/sedoc/services/audit/internal/repository"
)

// seedPreFixChain writes a chain the way the audit service did before the
// canonical-hash fix (07f13d71): the v2 hash covers the RAW envelope bytes,
// while the details column stores jsonb (keys reordered, spaces added), so
// VerifyIntegrity fails from the first event.
func seedPreFixChain(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant uuid.UUID, n int) {
	t.Helper()
	prev := ""
	at := time.Now().UTC().Truncate(time.Microsecond)
	for i := 0; i < n; i++ {
		e := &model.AuditEvent{
			ID: uuid.Must(uuid.NewV7()).String(), TenantID: tenant.String(),
			Actor: uuid.Must(uuid.NewV7()).String(), Action: "document.viewed",
			Details:     []byte(`{"type":"document.viewed","data":{"z":1,"a":2}}`),
			SourceEvent: "dms.document.viewed.v1", CreatedAt: at.Add(time.Duration(i) * time.Millisecond),
		}
		e.PreviousHash = prev
		e.EventHash = computeEventHash(prev, e)
		_, err := pool.Exec(ctx, `
			INSERT INTO audit_events (id, tenant_id, event_hash, previous_hash, actor, actor_id, actor_name,
			                          actor_type, action, details, source_event, created_at)
			VALUES ($1,$2,$3,$4,$5::text,$5::uuid,'','user',$6,$7,$8,$9)`,
			e.ID, e.TenantID, e.EventHash, e.PreviousHash, e.Actor, e.Action, string(e.Details), e.SourceEvent, e.CreatedAt)
		require.NoError(t, err)
		prev = e.EventHash
	}
}

// farFuture is a cutover after every seeded event (they are all "pre-fix").
var farFuture = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)

func snapshotHashes(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant uuid.UUID) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT id::text || ':' || event_hash || ':' || COALESCE(previous_hash,'')
		FROM audit_events WHERE tenant_id = $1 ORDER BY created_at, id`, tenant)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	return out
}

// H-12 round 3: the re-baseline defaults to a dry run that reports how
// many events would change and writes nothing.
func TestRebaseline_DryRunWritesNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool := newAuditPool(ctx, t)
	svc := New(Config{Repo: repository.New(pool), Logger: zerolog.Nop(), SigningKeySeedB64: newSeedB64()})
	tenant := uuid.New()
	seedPreFixChain(ctx, t, pool, tenant, 4)
	before := snapshotHashes(ctx, t, pool, tenant)

	res, err := svc.Rebaseline(ctx, tenant.String(), false, "ops@example.com", nil)
	require.NoError(t, err)
	require.Equal(t, 4, res.Events)
	require.Equal(t, 4, res.Rewritten, "every pre-fix event hashes differently in canonical form")
	require.False(t, res.Applied)

	require.Equal(t, before, snapshotHashes(ctx, t, pool, tenant), "a dry run must not write")
	cps, err := svc.ListCheckpoints(ctx, tenant.String(), 10)
	require.NoError(t, err)
	require.Empty(t, cps)
}

// H-12 round 3: --apply rewrites the chain into canonical form, appends an
// audit.chain_rebaselined marker and signs a checkpoint; a second run finds
// nothing to do and writes nothing.
func TestRebaseline_ApplyMakesChainVerifiable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool := newAuditPool(ctx, t)
	svc := New(Config{Repo: repository.New(pool), Logger: zerolog.Nop(), SigningKeySeedB64: newSeedB64()})
	tenant := uuid.New()
	seedPreFixChain(ctx, t, pool, tenant, 4)

	res, err := svc.VerifyIntegrity(ctx, tenant.String())
	require.NoError(t, err)
	require.False(t, res.Valid, "precondition: a pre-fix chain does not verify")

	rb, err := svc.Rebaseline(ctx, tenant.String(), true, "ops@example.com", &farFuture)
	require.NoError(t, err)
	require.True(t, rb.Applied)
	require.Equal(t, 4, rb.Rewritten)
	require.NotEqual(t, rb.OldHead, rb.NewHead)
	require.True(t, rb.Checkpointed)

	res, err = svc.VerifyIntegrity(ctx, tenant.String())
	require.NoError(t, err)
	require.True(t, res.Valid, "broken at %s", res.BrokenAt)
	require.EqualValues(t, 5, res.Verified, "4 events + the re-baseline marker")
	require.True(t, res.CheckpointValid, res.CheckpointMessage)

	var actorName, details string
	require.NoError(t, pool.QueryRow(ctx, `SELECT actor_name, details::text FROM audit_events
		WHERE tenant_id = $1 AND action = 'audit.chain_rebaselined'`, tenant).Scan(&actorName, &details))
	require.Equal(t, "ops@example.com", actorName)
	require.Contains(t, details, rb.OldHead)
	require.Contains(t, details, rb.NewHead)
	require.Contains(t, details, `"rewritten_count": 4`)

	// Idempotent: a second run reports nothing and writes nothing.
	before := snapshotHashes(ctx, t, pool, tenant)
	rb2, err := svc.Rebaseline(ctx, tenant.String(), true, "ops@example.com", &farFuture)
	require.NoError(t, err)
	require.Zero(t, rb2.Rewritten)
	require.False(t, rb2.Checkpointed)
	require.Equal(t, before, snapshotHashes(ctx, t, pool, tenant))
	cps, err := svc.ListCheckpoints(ctx, tenant.String(), 10)
	require.NoError(t, err)
	require.Len(t, cps, 1)
}
