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
