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

	res, err := svc.Rebaseline(ctx, tenant.String(), RebaselineOptions{Apply: false, Operator: "ops@example.com", Cutover: nil})
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

	rb, err := svc.Rebaseline(ctx, tenant.String(), RebaselineOptions{Apply: true, Operator: "ops@example.com", Cutover: &farFuture})
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
	rb2, err := svc.Rebaseline(ctx, tenant.String(), RebaselineOptions{Apply: true, Operator: "ops@example.com", Cutover: &farFuture})
	require.NoError(t, err)
	require.Zero(t, rb2.Rewritten)
	require.False(t, rb2.Checkpointed)
	require.Equal(t, before, snapshotHashes(ctx, t, pool, tenant))
	cps, err := svc.ListCheckpoints(ctx, tenant.String(), 10)
	require.NoError(t, err)
	require.Len(t, cps, 1)
}

// H-12 round 7: a tenant with no checkpoint at all cannot be cross-checked
// against anything signed (a deleted-checkpoints incident looks exactly
// like this). An --apply run that rewrites nothing refuses it, flagging
// NoCheckpoint, unless the first checkpoint is explicitly opted into.
func TestRebaseline_NoCheckpointResignNeedsOptIn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool := newAuditPool(ctx, t)
	svc := New(Config{Repo: repository.New(pool), Logger: zerolog.Nop(), SigningKeySeedB64: newSeedB64()})
	tenant := uuid.New()
	seedPreFixChain(ctx, t, pool, tenant, 3)
	_, err := svc.Rebaseline(ctx, tenant.String(), RebaselineOptions{Apply: true, Operator: "ops", Cutover: &farFuture})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM audit_checkpoints WHERE tenant_id = $1`, tenant)
	require.NoError(t, err)

	rb, err := svc.Rebaseline(ctx, tenant.String(), RebaselineOptions{Apply: true, Operator: "ops", Cutover: &farFuture})
	require.ErrorIs(t, err, ErrUncertifiedHead)
	require.True(t, rb.NoCheckpoint)
	require.False(t, rb.Checkpointed)
	cps, err := svc.ListCheckpoints(ctx, tenant.String(), 10)
	require.NoError(t, err)
	require.Empty(t, cps, "nothing may be signed without the opt-in")

	rb, err = svc.Rebaseline(ctx, tenant.String(), RebaselineOptions{Apply: true, Operator: "ops", Cutover: &farFuture,
		AllowFirstCheckpoint: true})
	require.NoError(t, err)
	require.True(t, rb.Checkpointed)
	res, err := svc.VerifyIntegrity(ctx, tenant.String())
	require.NoError(t, err)
	require.True(t, res.Valid)
	require.True(t, res.CheckpointValid, res.CheckpointMessage)

	// With the head covered, a further run writes nothing.
	rb, err = svc.Rebaseline(ctx, tenant.String(), RebaselineOptions{Apply: true, Operator: "ops", Cutover: &farFuture})
	require.NoError(t, err)
	require.False(t, rb.Checkpointed)
	cps, err = svc.ListCheckpoints(ctx, tenant.String(), 10)
	require.NoError(t, err)
	require.Len(t, cps, 1)
}

// H-12 round 7: the re-baseline's checkpoint is signed and inserted in the
// same transaction as the rewrite and the marker. If the checkpoint insert
// fails, the rewrite and the marker roll back with it: the chain is left
// exactly as it was, so no "rewritten but unsigned" state exists to repair.
func TestRebaseline_CheckpointIsAtomicWithRewrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool := newAuditPool(ctx, t)
	svc := New(Config{Repo: repository.New(pool), Logger: zerolog.Nop(), SigningKeySeedB64: newSeedB64()})
	tenant := uuid.New()
	seedPreFixChain(ctx, t, pool, tenant, 3)
	before := snapshotHashes(ctx, t, pool, tenant)

	// Inject a checkpoint-insert failure.
	_, err := pool.Exec(ctx, `
		CREATE FUNCTION test_fail_checkpoint() RETURNS trigger LANGUAGE plpgsql AS
		$$ BEGIN RAISE EXCEPTION 'injected checkpoint failure'; END $$;
		CREATE TRIGGER test_fail_checkpoint BEFORE INSERT ON audit_checkpoints
		FOR EACH ROW EXECUTE FUNCTION test_fail_checkpoint();`)
	require.NoError(t, err)

	rb, err := svc.Rebaseline(ctx, tenant.String(), RebaselineOptions{Apply: true, Operator: "ops", Cutover: &farFuture})
	require.ErrorContains(t, err, "injected checkpoint failure")
	require.False(t, rb.Checkpointed)
	require.Equal(t, before, snapshotHashes(ctx, t, pool, tenant), "the rewrite and the marker must roll back")
	var markers int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE tenant_id = $1 AND action = 'audit.chain_rebaselined'`, tenant).Scan(&markers))
	require.Zero(t, markers)

	// With the failure gone, the same run completes and is anchored.
	_, err = pool.Exec(ctx, `DROP TRIGGER test_fail_checkpoint ON audit_checkpoints`)
	require.NoError(t, err)
	rb, err = svc.Rebaseline(ctx, tenant.String(), RebaselineOptions{Apply: true, Operator: "ops", Cutover: &farFuture})
	require.NoError(t, err)
	require.True(t, rb.Applied)
	require.True(t, rb.Checkpointed)
	res, err := svc.VerifyIntegrity(ctx, tenant.String())
	require.NoError(t, err)
	require.True(t, res.Valid, "broken at %s", res.BrokenAt)
	require.True(t, res.CheckpointValid, res.CheckpointMessage)
}

// H-12 round 6: someone with DB write access but no signing key can
// rewrite a whole chain consistently (it then verifies, so Rewritten ==
// 0) — and the re-sign path used to sign that forged head on a routine
// --apply. The latest signed checkpoint's prefix no longer matches, so
// the tenant must be refused with nothing written.
func TestRebaseline_RefusesToSignRewrittenChain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool := newAuditPool(ctx, t)
	repo := repository.New(pool)
	svc := New(Config{Repo: repo, Logger: zerolog.Nop(), SigningKeySeedB64: newSeedB64()})
	tenant := uuid.New()
	for i := 0; i < 3; i++ {
		require.NoError(t, svc.RecordAccessDenied(ctx, tenant.String(), "", "", "read", "/x", "member"))
	}
	_, err := svc.CreateCheckpoint(ctx, tenant.String())
	require.NoError(t, err)

	// The forgery: change history and recompute every hash consistently.
	_, err = pool.Exec(ctx, `UPDATE audit_events SET action = 'audit.forged' WHERE tenant_id = $1`, tenant)
	require.NoError(t, err)
	events, err := repo.ListAll(ctx, tenant.String())
	require.NoError(t, err)
	prev := ""
	for _, e := range events {
		h := computeEventHash(prev, e)
		_, err = pool.Exec(ctx, `UPDATE audit_events SET previous_hash = $3, event_hash = $4 WHERE tenant_id = $1 AND id = $2`,
			tenant, e.ID, prev, h)
		require.NoError(t, err)
		prev = h
	}

	rb, err := svc.Rebaseline(ctx, tenant.String(), RebaselineOptions{Apply: true, Operator: "ops", Cutover: &farFuture})
	require.ErrorIs(t, err, ErrUncertifiedHead)
	require.Zero(t, rb.Rewritten, "the forged chain is internally consistent")
	require.False(t, rb.Checkpointed)
	cps, err := svc.ListCheckpoints(ctx, tenant.String(), 10)
	require.NoError(t, err)
	require.Len(t, cps, 1, "the forged head must not be signed")
}

// H-12 round 7: the round-6 repair exception accepted any row whose action
// was audit.chain_rebaselined, and anyone with DB write access can insert
// one. A consistent rewrite plus a forged marker after the signed prefix
// must still be refused, with nothing signed.
func TestRebaseline_ForgedMarkerDoesNotAllowResign(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool := newAuditPool(ctx, t)
	repo := repository.New(pool)
	svc := New(Config{Repo: repo, Logger: zerolog.Nop(), SigningKeySeedB64: newSeedB64()})
	tenant := uuid.New()
	for i := 0; i < 3; i++ {
		require.NoError(t, svc.RecordAccessDenied(ctx, tenant.String(), "", "", "read", "/x", "member"))
	}
	_, err := svc.CreateCheckpoint(ctx, tenant.String())
	require.NoError(t, err)

	// The forgery: change history, append a fake re-baseline marker after
	// the signed prefix, and recompute every hash consistently.
	_, err = pool.Exec(ctx, `UPDATE audit_events SET action = 'audit.forged' WHERE tenant_id = $1`, tenant)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO audit_events (id, tenant_id, event_hash, previous_hash, actor, actor_type, actor_name,
		                          action, details, created_at)
		VALUES ($1, $2, 'x', '', '', 'system', 'ops', 'audit.chain_rebaselined', '{}', now() + interval '1 second')`,
		uuid.Must(uuid.NewV7()), tenant)
	require.NoError(t, err)
	events, err := repo.ListAll(ctx, tenant.String())
	require.NoError(t, err)
	require.Len(t, events, 4)
	prev := ""
	for _, e := range events {
		h := computeEventHash(prev, e)
		_, err = pool.Exec(ctx, `UPDATE audit_events SET previous_hash = $3, event_hash = $4 WHERE tenant_id = $1 AND id = $2`,
			tenant, e.ID, prev, h)
		require.NoError(t, err)
		prev = h
	}

	rb, err := svc.Rebaseline(ctx, tenant.String(), RebaselineOptions{Apply: true, Operator: "ops", Cutover: &farFuture})
	require.ErrorIs(t, err, ErrUncertifiedHead)
	require.Zero(t, rb.Rewritten, "the forged chain is internally consistent")
	require.False(t, rb.Checkpointed)
	cps, err := svc.ListCheckpoints(ctx, tenant.String(), 10)
	require.NoError(t, err)
	require.Len(t, cps, 1, "the forged head must not be signed")
}
