//go:build integration
// +build integration

// H-13 (2026-10-12 review) — connection and timeout publish errors must
// not count toward MaxAttempts. Before the fix every failure counted, so
// a NATS outage of a few seconds dead-lettered every pending event.
package database_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

// fakeJS fails PublishMsg with a per-subject error until cleared.
type fakeJS struct {
	nats.JetStreamContext
	mu   sync.Mutex
	errs map[string]error
	sent map[string]int
}

func (f *fakeJS) PublishMsg(m *nats.Msg, _ ...nats.PubOpt) (*nats.PubAck, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs[m.Subject]; err != nil {
		return nil, err
	}
	f.sent[m.Subject]++
	return &nats.PubAck{Stream: "FAKE"}, nil
}

func (f *fakeJS) clear(subject string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.errs, subject)
}

func newSuperOutboxPool(ctx context.Context, t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn, cleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	require.NoError(t, database.RunMigrations(dsn, documentMigrationsDir))
	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true // container superuser
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func insertOutboxRow(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant uuid.UUID, subject string) uuid.UUID {
	t.Helper()
	evt := database.NewOutboxEvent(tenant, subject, "document", uuid.Must(uuid.NewV7()),
		json.RawMessage(`{"k":"v"}`))
	require.NoError(t, database.WithTenantTx(ctx, pool, tenant, func(tx pgx.Tx) error {
		return database.NewOutboxRepository().Insert(ctx, tx, evt)
	}))
	return evt.ID
}

func TestPublisher_TransientErrorsDoNotDeadLetter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)
	pool := newSuperOutboxPool(ctx, t)
	tenant := uuid.Must(uuid.NewV7())

	transient := map[string]error{
		"dms.transient.timeout.v1":      fmt.Errorf("js publish: %w", nats.ErrTimeout),
		"dms.transient.noresponders.v1": nats.ErrNoResponders,
		"dms.transient.closed.v1":       nats.ErrConnectionClosed,
		"dms.transient.reconnecting.v1": nats.ErrConnectionReconnecting,
		"dms.transient.deadline.v1":     context.DeadlineExceeded,
		"dms.transient.disconnected.v1": nats.ErrDisconnected,
	}
	const permanentSubject = "dms.permanent.nostream.v1"
	js := &fakeJS{errs: map[string]error{permanentSubject: nats.ErrNoStreamResponse}, sent: map[string]int{}}
	ids := map[string]uuid.UUID{}
	for subject, err := range transient {
		js.errs[subject] = err
		ids[subject] = insertOutboxRow(ctx, t, pool, tenant, subject)
	}
	permanent := insertOutboxRow(ctx, t, pool, tenant, permanentSubject)

	// MaxAttempts=1: any COUNTED failure dead-letters on the spot. A short
	// backoff lets each deferred row yield to the ones behind it, so every
	// row is retried many times within the test.
	pub := database.NewOutboxPublisherWithConfig(pool, js, "transient", zerolog.Nop(),
		database.PublisherConfig{PollInterval: 10 * time.Millisecond, MaxAttempts: 1, BaseBackoff: 20 * time.Millisecond})
	pctx, pcancel := context.WithCancel(ctx)
	go pub.Start(pctx)
	t.Cleanup(func() { pcancel(); pub.Stop() })

	// The permanent failure is the control: it must dead-letter.
	require.Eventually(t, func() bool {
		var n int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM outbox_dlq WHERE id = $1`, permanent).Scan(&n)
		return n == 1
	}, 10*time.Second, 20*time.Millisecond, "a permanent publish error must still dead-letter")

	// Let the transient rows fail through many more poll cycles.
	for subject, id := range ids {
		require.Eventually(t, func() bool {
			var lastErr *string
			_ = pool.QueryRow(ctx, `SELECT last_error FROM outbox WHERE id = $1`, id).Scan(&lastErr)
			return lastErr != nil && *lastErr != ""
		}, 10*time.Second, 20*time.Millisecond, "%s: transient failure must be recorded", subject)
	}
	time.Sleep(300 * time.Millisecond)

	for subject, id := range ids {
		var dlq, attempts int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM outbox_dlq WHERE id = $1`, id).Scan(&dlq))
		require.Zerof(t, dlq, "%s: a transient error must never dead-letter", subject)
		require.NoError(t, pool.QueryRow(ctx, `SELECT attempts FROM outbox WHERE id = $1`, id).Scan(&attempts))
		require.Zerof(t, attempts, "%s: a transient error must not count toward MaxAttempts", subject)
	}

	// NATS comes back: every held row publishes.
	for subject := range ids {
		js.clear(subject)
	}
	for subject, id := range ids {
		require.Eventually(t, func() bool {
			var published bool
			_ = pool.QueryRow(ctx, `SELECT published FROM outbox WHERE id = $1`, id).Scan(&published)
			return published
		}, 10*time.Second, 20*time.Millisecond, "%s: row must publish once NATS recovers", subject)
	}
	require.False(t, errors.Is(nats.ErrNoStreamResponse, nats.ErrNoResponders),
		"an unbound subject (ErrNoStreamResponse) must stay a counted failure")
}

// A transient failure defers a row by BaseBackoff, growing only with that
// row's consecutive transient failures — never with the row's age. An
// age-keyed deferral parked a 3-minute-old row for 3 minutes after a single
// blip, kept the queue waiting up to MaxBackoff after NATS recovered, and
// pushed retries past JetStream's 2-minute duplicate window. Deferred rows
// must also stay visible in the drain-lag metric.
func TestPublisher_TransientDeferralIndependentOfRowAge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)
	pool := newSuperOutboxPool(ctx, t)
	tenant := uuid.Must(uuid.NewV7())

	const subject = "dms.transient.age.v1"
	js := &fakeJS{errs: map[string]error{subject: fmt.Errorf("js publish: %w", nats.ErrTimeout)}, sent: map[string]int{}}
	old := uuid.Must(uuid.NewV7())
	_, err := pool.Exec(ctx, `
		INSERT INTO outbox (id, tenant_id, event_type, aggregate_type, aggregate_id, payload, created_at)
		VALUES ($1, $2, $3, 'document', gen_random_uuid(), '{"k":"v"}', now() - interval '3 hours')`,
		old, tenant, subject)
	require.NoError(t, err)
	fresh := insertOutboxRow(ctx, t, pool, tenant, subject)

	// Default config: BaseBackoff 2s, MaxBackoff 5m.
	pub := database.NewOutboxPublisherWithConfig(pool, js, "transient-age", zerolog.Nop(),
		database.PublisherConfig{PollInterval: 20 * time.Millisecond})
	pctx, pcancel := context.WithCancel(ctx)
	go pub.Start(pctx)
	t.Cleanup(func() { pcancel(); pub.Stop() })

	// firstDeferral waits for the row's first transient failure and returns
	// how far in the future its next attempt is scheduled.
	firstDeferral := func(id uuid.UUID) float64 {
		t.Helper()
		var secs float64
		require.Eventually(t, func() bool {
			var attempts int
			var lastErr *string
			if err := pool.QueryRow(ctx, `
				SELECT attempts, last_error,
				       COALESCE(EXTRACT(EPOCH FROM (next_attempt_at - now()))::float8, 0)
				  FROM outbox WHERE id = $1`, id).Scan(&attempts, &lastErr, &secs); err != nil {
				return false
			}
			require.Zero(t, attempts, "a transient failure must not count as an attempt")
			return lastErr != nil
		}, 15*time.Second, 10*time.Millisecond, "row %s never failed", id)
		return secs
	}

	for name, id := range map[string]uuid.UUID{"3h-old row": old, "fresh row": fresh} {
		d := firstDeferral(id)
		require.Greaterf(t, d, 0.5, "%s: first transient deferral", name)
		require.LessOrEqualf(t, d, 2.1, "%s: first transient deferral must be BaseBackoff (2s), not the row's age", name)
	}

	// The drain-lag gauge must count the deferred rows: the 3h-old row is
	// still unpublished.
	var lag *float64
	require.NoError(t, pool.QueryRow(ctx, `SELECT drain_lag_seconds FROM outbox_drain_stats()`).Scan(&lag))
	require.NotNil(t, lag, "deferred unpublished rows must keep drain lag non-null")
	require.Greater(t, *lag, float64(3*time.Hour/time.Second)-60, "drain lag must include backoff-deferred rows")

	// A second consecutive transient failure doubles the deferral; the
	// count lives on the row, not in attempts.
	var secs float64
	require.Eventually(t, func() bool {
		var failures, attempts int
		if err := pool.QueryRow(ctx, `
			SELECT transient_failures, attempts,
			       EXTRACT(EPOCH FROM (next_attempt_at - now()))::float8
			  FROM outbox WHERE id = $1`, old).Scan(&failures, &attempts, &secs); err != nil {
			return false
		}
		require.Zero(t, attempts)
		return failures == 2
	}, 15*time.Second, 10*time.Millisecond, "the old row never failed a second time")
	require.Greater(t, secs, 2.5)
	require.LessOrEqual(t, secs, 4.1, "second transient deferral must be 2×BaseBackoff")
}
